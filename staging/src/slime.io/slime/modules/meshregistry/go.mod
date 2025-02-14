module slime.io/slime/modules/meshregistry

go 1.13

require (
	github.com/go-zookeeper/zk v0.0.0
	github.com/gogo/protobuf v1.3.2
	github.com/golang/protobuf v1.5.2
	github.com/hashicorp/go-multierror v1.1.1
	github.com/nacos-group/nacos-sdk-go/v2 v2.1.2
	github.com/orcaman/concurrent-map v0.0.0-20210106121528-16402b402231
	github.com/sirupsen/logrus v1.8.1
	google.golang.org/grpc v1.48.0
	gopkg.in/yaml.v2 v2.4.0
	istio.io/api v0.0.0-20210322145030-ec7ef4cd6eaf
	istio.io/istio v0.0.0-00010101000000-000000000000
	istio.io/istio-mcp v0.0.0
	istio.io/pkg v0.0.0-20200807181912-d97bc429be20
	k8s.io/api v0.22.2
	k8s.io/apimachinery v0.22.2
	k8s.io/client-go v0.22.2
	k8s.io/kube-openapi v0.0.0-20210421082810-95288971da7e
	sigs.k8s.io/yaml v1.2.0
	slime.io/slime/framework v0.0.0
)

replace (
	github.com/docker/distribution => github.com/docker/distribution v2.7.1+incompatible

	// Avoid pulling in incompatible libraries
	github.com/docker/docker => github.com/docker/engine v1.4.2-0.20191011211953-adfac697dc5b

	github.com/envoyproxy/go-control-plane => github.com/envoyproxy/go-control-plane v0.9.5-0.20200326174812-e8bd2869ff56

	github.com/go-zookeeper/zk => github.com/slime-io/go-zk v0.0.0-20220815024955-60853abc02f4

	istio.io/api => istio.io/api v0.0.0-20211206163441-1a632586cbd4
	istio.io/istio => github.com/slime-io/istio v0.0.0-20221206095013-731766b8cb10
	istio.io/istio-mcp => github.com/slime-io/istio-mcp v0.0.0-20221025054010-d76433980485

	slime.io/slime/framework => ../../../../../../framework
)
