package apply

import (
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/scale/scheme"
)

var Scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(scheme.AddToScheme(Scheme))
}
