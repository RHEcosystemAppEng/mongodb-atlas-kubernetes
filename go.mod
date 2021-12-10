module github.com/mongodb/mongodb-atlas-kubernetes

go 1.15

require (
	github.com/RHEcosystemAppEng/dbaas-operator v1.0.0
	github.com/fatih/structtag v1.2.0
	github.com/fgrosse/zaptest v1.1.0
	github.com/go-logr/zapr v0.4.0
	github.com/golang/snappy v0.0.2 // indirect
	github.com/google/go-cmp v0.5.6
	github.com/gorilla/mux v1.8.0
	github.com/mongodb-forks/digest v1.0.2
	github.com/mxschmitt/playwright-go v0.1100.0
	github.com/onsi/ginkgo v1.16.4
	github.com/onsi/gomega v1.14.0
	github.com/pborman/uuid v1.2.1
	github.com/sethvargo/go-password v0.2.0
	github.com/stretchr/testify v1.7.0
	go.mongodb.org/atlas v0.7.3-0.20210315115044-4b1d3f428c24
	go.mongodb.org/mongo-driver v1.7.0
	go.uber.org/zap v1.18.1
	golang.org/x/sys v0.0.0-20210630005230-0f9fa26af87c // indirect
	golang.org/x/tools v0.1.5 // indirect
	gopkg.in/yaml.v3 v3.0.0-20210107192922-496545a6307b
	k8s.io/api v0.21.1
	k8s.io/apimachinery v0.21.1
	k8s.io/client-go v0.21.1
	k8s.io/utils v0.0.0-20210709001253-0e1f9d693477
	sigs.k8s.io/controller-runtime v0.9.0
)

// replace github.com/RHEcosystemAppEng/dbaas-operator v1.0.0 => github.com/RHEcosystemAppEng/dbaas-operator v1.0.1-0.20210728155403-fcf7cfba855c
replace github.com/RHEcosystemAppEng/dbaas-operator v1.0.0 => github.com/xieshenzh/dbaas-operator v1.0.1-0.20220110220154-ce3fb7f17e6e
