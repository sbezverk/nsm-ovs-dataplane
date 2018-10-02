package ovsinterface

import (
	"errors"
	"reflect"

	"github.com/sirupsen/logrus"
	"github.com/socketplane/libovsdb"
)

// Interface defines ovs daplane functions
type Interface interface {
	CreateBridge(bridgeName string) error
	DeleteBridge(bridgeName string) error
	ListBridges()
}

// OVSDataplane defines a structure of info used in nsm ovs dataplane
type OVSDataplane struct {
	ovs          *libovsdb.OvsdbClient
	socket       string
	notifier     *notifier
	tableUpdates *libovsdb.TableUpdates
}

// NewOVSDataplane creates a new instance of OVS dataplane
func NewOVSDataplane(socket string) (Interface, error) {

	// By default libovsdb connects to 127.0.0.0:6400.
	ovs, err := libovsdb.ConnectWithUnixSocket("/var/run/openvswitch/db.sock")
	if err != nil {
		return nil, err
	}
	ovsDataplane := &OVSDataplane{
		ovs:    ovs,
		socket: socket,
	}
	ovsDataplane.notifier = newNotifier()
	ovs.Register(ovsDataplane.notifier)

	ovsDataplane.tableUpdates, err = ovs.MonitorAll("Open_vSwitch", "")
	if err != nil {
		return nil, err
	}
	ovsDataplane.notifier.populateCache(*ovsDataplane.tableUpdates)

	return ovsDataplane, nil
}

// ListBridges lists all bridges found in the cache
func (o *OVSDataplane) ListBridges() {
	for uuid, row := range o.notifier.cache["Bridge"] {
		name := row.Fields["name"].(string)
		logrus.Infof("Bridge name: %s uuid: %s", name, uuid)
	}
}

// Wrapper for ovsDB transaction
func (o *OVSDataplane) ovsdbTransact(ops []libovsdb.Operation) error {
	// Print out what we are sending
	logrus.Infof("Transaction: %+v\n", ops)

	// Perform OVSDB transaction
	reply, _ := o.ovs.Transact("Open_vSwitch", ops...)

	if len(reply) < len(ops) {
		logrus.Errorf("Unexpected number of replies. Expected: %d, Recvd: %d", len(ops), len(reply))
		return errors.New("OVS transaction failed. Unexpected number of replies")
	}

	// Parse reply and look for errors
	for i, o := range reply {
		if o.Error != "" && i < len(ops) {
			return errors.New("OVS Transaction failed err " + o.Error + "Details: " + o.Details)
		} else if o.Error != "" {
			return errors.New("OVS Transaction failed err " + o.Error + "Details: " + o.Details)
		}
	}

	// Return success
	return nil
}

// CreateBridge creates bridge of specified name
func (o *OVSDataplane) CreateBridge(bridgeName string) error {
	namedUUID := "nsmovsdataplane"
	// bridge row to insert
	bridge := make(map[string]interface{})
	bridge["name"] = bridgeName

	// simple insert operation
	insertOp := libovsdb.Operation{
		Op:       "insert",
		Table:    "Bridge",
		Row:      bridge,
		UUIDName: namedUUID,
	}

	// Inserting a Bridge row in Bridge table requires mutating the open_vswitch table.
	uuidParameter := libovsdb.UUID{GoUUID: o.notifier.getRootUUID()}
	mutateUUID := []libovsdb.UUID{
		{
			GoUUID: namedUUID,
		},
	}
	mutateSet, _ := libovsdb.NewOvsSet(mutateUUID)
	mutation := libovsdb.NewMutation("bridges", "insert", mutateSet)
	condition := libovsdb.NewCondition("_uuid", "==", uuidParameter)

	// simple mutate operation
	mutateOp := libovsdb.Operation{
		Op:        "mutate",
		Table:     "Open_vSwitch",
		Mutations: []interface{}{mutation},
		Where:     []interface{}{condition},
	}
	operations := []libovsdb.Operation{insertOp, mutateOp}

	return o.ovsdbTransact(operations)
}

// DeleteBridge creates bridge of specified name
func (o *OVSDataplane) DeleteBridge(bridgeName string) error {

	namedUUIDStr := "nsmovsdataplane"
	brUUID := []libovsdb.UUID{{GoUUID: namedUUIDStr}}

	// simple insert/delete operation
	brOp := libovsdb.Operation{}
	condition := libovsdb.NewCondition("name", "==", bridgeName)
	brOp = libovsdb.Operation{
		Op:    "delete",
		Table: "Bridge",
		Where: []interface{}{condition},
	}

	// also fetch the br-uuid from cache
	for uuid, row := range o.notifier.cache["Bridge"] {
		name := row.Fields["name"].(string)
		logrus.Infof("Checking name: %s and uuid: %s", name, uuid)
		if name == bridgeName {
			logrus.Infof("found bridge's uuid: %s", uuid)
			brUUID = []libovsdb.UUID{{GoUUID: uuid}}
			break
		}
	}

	// Inserting/Deleting a Bridge row in Bridge table requires mutating
	// the open_vswitch table.
	mutateUUID := brUUID
	mutateSet, _ := libovsdb.NewOvsSet(mutateUUID)
	mutation := libovsdb.NewMutation("bridges", "delete", mutateSet)
	condition = libovsdb.NewCondition("_uuid", "==", o.notifier.getRootUUID())

	// simple mutate operation
	mutateOp := libovsdb.Operation{
		Op:        "mutate",
		Table:     "Open_vSwitch",
		Mutations: []interface{}{mutation},
		Where:     []interface{}{condition},
	}

	operations := []libovsdb.Operation{brOp, mutateOp}
	return o.ovsdbTransact(operations)

	/*	namedUUID := "nsmovsdataplane"
		brUUID := []libovsdb.UUID{
			{
				GoUUID: namedUUID,
			},
		}
		// simple delete operation
		condition := libovsdb.NewCondition("name", "==", bridgeName)
		deleteOp := libovsdb.Operation{
			Op:    "delete",
			Table: "Bridge",
			Where: []interface{}{condition},
		}
		for uuid, row := range o.notifier.cache["Bridge"] {
			name := row.Fields["name"].(string)
			if name == bridgeName {
				brUUID = []libovsdb.UUID{{GoUUID: uuid}}
				break
			}
		}
		mutateUUID := brUUID
		mutateSet, _ := libovsdb.NewOvsSet(mutateUUID)
		mutation := libovsdb.NewMutation("bridges", "delete", mutateSet)
		condition = libovsdb.NewCondition("_uuid", "==", o.notifier.getRootUUID())

		// simple mutate operation
		mutateOp := libovsdb.Operation{
			Op:        "mutate",
			Table:     "Open_vSwitch",
			Mutations: []interface{}{mutation},
			Where:     []interface{}{condition},
		}

		operations := []libovsdb.Operation{deleteOp, mutateOp}
		return o.ovsdbTransact(operations)
	*/
}

func newNotifier() *notifier {
	return &notifier{
		cache:  make(map[string]map[string]libovsdb.Row),
		update: make(chan *libovsdb.TableUpdates),
	}
}

// notifier defines methods which will be called for different events
type notifier struct {
	update chan *libovsdb.TableUpdates
	cache  map[string]map[string]libovsdb.Row
}

func (n notifier) Update(context interface{}, tableUpdates libovsdb.TableUpdates) {
	n.populateCache(tableUpdates)
	n.update <- &tableUpdates
}
func (n notifier) Locked([]interface{}) {
}
func (n notifier) Stolen([]interface{}) {
}
func (n notifier) Echo([]interface{}) {
}
func (n notifier) Disconnected(client *libovsdb.OvsdbClient) {
}

func (n *notifier) getRootUUID() string {
	for uuid := range n.cache["Open_vSwitch"] {
		return uuid
	}
	return ""
}

func (n *notifier) populateCache(updates libovsdb.TableUpdates) {
	for table, tableUpdate := range updates.Updates {
		if _, ok := n.cache[table]; !ok {
			n.cache[table] = make(map[string]libovsdb.Row)

		}
		for uuid, row := range tableUpdate.Rows {
			empty := libovsdb.Row{}
			if !reflect.DeepEqual(row.New, empty) {
				n.cache[table][uuid] = row.New
			} else {
				delete(n.cache[table], uuid)
			}
		}
	}
}
