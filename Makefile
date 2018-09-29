REGISTRY_NAME = 192.168.80.240:4000/networkservicemesh
IMAGE_VERSION = latest

.PHONY: all nsm-ovs-dataplane container push clean test

ifdef V
TESTARGS = -v -args -alsologtostderr -v 5
else
TESTARGS =
endif

all: nsm-ovs-dataplane

nsm-ovs-dataplane:
	mkdir -p bin
	CGO_ENABLED=0 GOOS=linux go build -a -ldflags '-extldflags "-static"' -o ./bin/nsm-ovs-dataplane ./cmd/nsm-ovs-dataplane.go

nsm-ovs-dataplane-mac:
	mkdir -p bin
	CGO_ENABLED=0 GOOS=darwin go build -a -ldflags '-extldflags "-static"' -o ./bin/nsm-ovs-dataplane ./cmd/nsm-ovs-dataplane.go

container: nsm-ovs-dataplane
	docker build -t $(REGISTRY_NAME)/nsm-ovs-dataplane:$(IMAGE_VERSION) -f ./build/Dockerfile .

push: container
	docker push $(REGISTRY_NAME)/nsm-ovs-dataplane:$(IMAGE_VERSION)

clean:
	rm -rf bin

test:
	go test `go list ./... | grep -v 'vendor'` $(TESTARGS)
	go vet `go list ./... | grep -v vendor`
