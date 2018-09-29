// Copyright (c) 2018 Cisco and/or its affiliates.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at:
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"flag"
	"net"
	"os"
	"path"
	"runtime"
	"sync"
	"time"

	"github.com/ligato/networkservicemesh/pkg/nsm/apis/common"
	"github.com/ligato/networkservicemesh/pkg/nsm/apis/dataplaneinterface"
	dataplaneregistrarapi "github.com/ligato/networkservicemesh/pkg/nsm/apis/dataplaneregistrar"
	"github.com/ligato/networkservicemesh/pkg/tools"
	"github.com/ligato/networkservicemesh/plugins/dataplaneregistrar"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
)

const (
	dataplaneSocket           = "/var/lib/networkservicemesh/nsm-ovs.dataplane.sock"
	interfaceNameMaxLength    = 15
	registrationRetryInterval = 30
)

var (
	dataplane       = flag.String("dataplane-socket", dataplaneSocket, "Location of the dataplane gRPC socket")
	registrarSocket = path.Join(dataplaneregistrar.DataplaneRegistrarSocketBaseDir, dataplaneregistrar.DataplaneRegistrarSocket)
)

// DataplaneController keeps k8s client and gRPC server
type DataplaneController struct {
	dataplaneServer *grpc.Server
	updateCh        chan Update
}

// Update is a message used to communicate any changes in operational parameters and constraints
type Update struct {
	remoteMechanism []*common.RemoteMechanism
}

// livenessMonitor is a stream initiated by NSM to inform the dataplane that NSM is still alive and
// no re-registration is required. Detection a failure on this "channel" will mean
// that NSM is gone and the dataplane needs to start re-registration logic.
func livenessMonitor(registrationConnection dataplaneregistrarapi.DataplaneRegistrationClient) {
	stream, err := registrationConnection.RequestLiveness(context.Background())
	if err != nil {
		logrus.Errorf("test-dataplane: fail to create liveness grpc channel with NSM with error: %s, grpc code: %+v", err.Error(), status.Convert(err).Code())
		return
	}
	for {
		err := stream.RecvMsg(&common.Empty{})
		if err != nil {
			logrus.Errorf("test-dataplane: fail to receive from liveness grpc channel with error: %s, grpc code: %+v", err.Error(), status.Convert(err).Code())
			return
		}
	}
}

// UpdateDataplane implements method of dataplane interface, which is informing NSM of any changes
// to operational prameters or constraints
func (d DataplaneController) UpdateDataplane(empty *common.Empty, updateSrv dataplaneinterface.DataplaneOperations_UpdateDataplaneServer) error {
	logrus.Infof("Update dataplane was called")
	for {
		select {
		// Waiting for any updates which might occur during a life of dataplane module and communicating
		// them back to NSM.
		case update := <-d.updateCh:
			if err := updateSrv.Send(&dataplaneinterface.DataplaneUpdate{
				RemoteMechanism: update.remoteMechanism,
			}); err != nil {
				logrus.Errorf("test-dataplane: Deteced error %s, grpc code: %+v on grpc channel", err.Error(), status.Convert(err).Code())
				return nil
			}
		}
	}
}

func init() {
	runtime.LockOSThread()
}

func main() {
	var wg sync.WaitGroup

	flag.Parse()

	// Step 1: Start server on our dataplane socket, for now 2 API will be bound to this socket
	// DataplaneInterface API (eventually it will become the only API needed), currently used to notify
	// NSM for any changes in operational parameters and/or constraints and TestDataplane API which is used
	// to actually used to connect pods together.

	dataplaneController := DataplaneController{
		updateCh: make(chan Update),
	}

	socket := *dataplane
	if err := tools.SocketCleanup(socket); err != nil {
		logrus.Fatalf("test-dataplane: failure to cleanup stale socket %s with error: %+v", socket, err)
	}
	dataplaneConn, err := net.Listen("unix", socket)
	if err != nil {
		logrus.Fatalf("test-dataplane: fail to open socket %s with error: %+v", socket, err)
	}
	dataplaneController.dataplaneServer = grpc.NewServer()

	// Binding dataplane Interface API to gRPC server
	dataplaneinterface.RegisterDataplaneOperationsServer(dataplaneController.dataplaneServer, dataplaneController)

	go func() {
		wg.Add(1)
		if err := dataplaneController.dataplaneServer.Serve(dataplaneConn); err != nil {
			logrus.Fatalf("test-dataplane: failed to start grpc server on socket %s with error: %+v ", socket, err)
		}
	}()
	// Check if the socket of device plugin server is operation
	testSocket, err := tools.SocketOperationCheck(socket)
	if err != nil {
		logrus.Fatalf("test-dataplane: failure to communicate with the socket %s with error: %+v", socket, err)
	}
	testSocket.Close()
	logrus.Infof("test-dataplane: Test Dataplane controller is ready to serve...")
	for {
		// Step 2: The server is ready now dataplane needs to register with NSM.
		if _, err := os.Stat(registrarSocket); err != nil {
			logrus.Errorf("test-dataplane: failure to access nsm socket at %s with error: %+v, exiting...", registrarSocket, err)
			time.Sleep(time.Second * registrationRetryInterval)
			continue
		}

		conn, err := tools.SocketOperationCheck(registrarSocket)
		if err != nil {
			logrus.Errorf("test-dataplane: failure to communicate with the socket %s with error: %+v", registrarSocket, err)
			time.Sleep(time.Second * registrationRetryInterval)
			continue
		}
		defer conn.Close()
		logrus.Infof("test-dataplane: connection to dataplane registrar socket % succeeded.", registrarSocket)

		registrarConnection := dataplaneregistrarapi.NewDataplaneRegistrationClient(conn)
		dataplane := dataplaneregistrarapi.DataplaneRegistrationRequest{
			DataplaneName:   "nsm-ovs-dataplane",
			DataplaneSocket: socket,
			RemoteMechanism: []*common.RemoteMechanism{},
			SupportedInterface: []*common.Interface{
				{
					Type: common.InterfaceType_KERNEL_INTERFACE,
				},
			},
		}
		if _, err := registrarConnection.RequestDataplaneRegistration(context.Background(), &dataplane); err != nil {
			dataplaneController.dataplaneServer.Stop()
			logrus.Fatalf("test-dataplane: failure to communicate with the socket %s with error: %+v", registrarSocket, err)
			time.Sleep(time.Second * registrationRetryInterval)
			continue
		}
		logrus.Infof("test-dataplane: dataplane has successfully been registered, waiting for connection from NSM...")
		// Block on Liveness stream until NSM is gone, if failure of NSM is detected
		// go to a re-registration
		livenessMonitor(registrarConnection)
	}
}
