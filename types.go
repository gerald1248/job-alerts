package main

import (
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"sync"
)

// Controller represents the controller state
type Controller struct {
	indexer   cache.Indexer
	queue     workqueue.RateLimitingInterface
	informer  cache.Controller
	clientset kubernetes.Interface
	mutex     *sync.Mutex
	state     State
}

type State struct {
}

type LogObject struct {
	Job       string `json:"job"`
	Pod       string `json:"pod"`
	Namespace string `json:"namespace"`
	Phase     string `json:"phase"`
}
