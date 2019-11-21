package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/workqueue"

	v1 "k8s.io/api/core/v1"
	rest "k8s.io/client-go/rest"

	au "github.com/logrusorgru/aurora"
)

// NewController constructs the central controller state
func NewController(queue workqueue.RateLimitingInterface, indexer cache.Indexer, informer cache.Controller, clientset kubernetes.Interface, mutex *sync.Mutex, state State) *Controller {
	return &Controller{
		informer:  informer,
		indexer:   indexer,
		queue:     queue,
		clientset: clientset,
		mutex:     mutex,
		state:     state,
	}
}

func (c *Controller) processNextItem() bool {
	key, quit := c.queue.Get()
	if quit {
		return false
	}
	defer c.queue.Done(key)

	err := c.syncToStdout(key.(string))
	c.handleErr(err, key)

	return true
}

func (c *Controller) syncToStdout(key string) error {
	obj, _, err := c.indexer.GetByKey(key)
	if err != nil {
		log.Println(fmt.Sprintf("%s: fetching object with key %s from store failed with %v", au.Bold(au.Red("Error")), key, err))
		return err
	}

	// exit condition 1: nil interface received (e.g. after manual resource deletion)
	// nothing to do here; return gracefully
	if obj == nil {
		return nil
	}

	labels := obj.(*v1.Pod).ObjectMeta.Labels

	job, ok := labels["job-name"]

	if !ok {
		return nil
	}

	phase := string(obj.(*v1.Pod).Status.Phase)

	if phase != "Succeeded" && phase != "Failed" {
		return nil
	}

	var logObject LogObject
	logObject.Job = job
	logObject.Pod = obj.(*v1.Pod).ObjectMeta.Name
	logObject.Namespace = obj.(*v1.Pod).ObjectMeta.Namespace
	logObject.Phase = phase

	bytes, err := json.Marshal(logObject)
	if err != nil {
		log.Println(fmt.Sprintf("%s: %s", au.Bold(au.Red("Error")), au.Bold(err)))
		return nil
	}

	// main JSON output goes to stdout
	fmt.Printf("%s\n", bytes)

	if c.queue.Len() == 0 {
		// TODO: is this significant here?
	}

	return nil
}

// handleErr checks if an error happened and makes sure we will retry later.
func (c *Controller) handleErr(err error, key interface{}) {
	if err == nil {
		c.queue.Forget(key)
		return
	}

	if c.queue.NumRequeues(key) < 5 {
		log.Println(fmt.Sprintf("%s: can't sync pod %v: %v", au.Bold(au.Red("Error")), key, err))
		c.queue.AddRateLimited(key)
		return
	}

	c.queue.Forget(key)
	runtime.HandleError(err)
	log.Println(fmt.Sprintf("%s: dropping pod %q from the queue: %v", au.Bold(au.Cyan("INFO")), key, err))
}

// Run manages the controller lifecycle
func (c *Controller) Run(threadiness int, stopCh chan struct{}) {
	defer runtime.HandleCrash()

	defer c.queue.ShutDown()
	log.Println(fmt.Sprintf("%s: starting job alerts", au.Bold(au.Cyan("INFO"))))

	go c.informer.Run(stopCh)

	if !cache.WaitForCacheSync(stopCh, c.informer.HasSynced) {
		runtime.HandleError(fmt.Errorf("Timed out waiting for caches to sync"))
		return
	}

	for i := 0; i < threadiness; i++ {
		go wait.Until(c.runWorker, time.Second, stopCh)
	}

	<-stopCh
	log.Println(fmt.Sprintf("%s: stopping job alerts", au.Bold(au.Cyan("INFO"))))
}

func (c *Controller) runWorker() {
	for c.processNextItem() {
	}
}

func main() {
	var kubeconfig string
	var master string

	flag.StringVar(&kubeconfig, "kubeconfig", "", "absolute path to the kubeconfig file")
	flag.StringVar(&master, "master", "", "master url")
	flag.Parse()

	// support out-of-cluster deployments (param, env var only)
	if len(kubeconfig) == 0 {
		kubeconfig = os.Getenv("KUBECONFIG")
	}

	var config *rest.Config
	var configError error

	if len(kubeconfig) > 0 {
		config, configError = clientcmd.BuildConfigFromFlags(master, kubeconfig)
		if configError != nil {
			fmt.Fprintf(os.Stderr, "%s: %s\n", au.Bold(au.Red("Out-of-cluster error")), configError)
			return
		}
	} else {
		config, configError = rest.InClusterConfig()
		if configError != nil {
			fmt.Fprintf(os.Stderr, "%s: %s\n", au.Bold(au.Red("In-cluster error")), configError)
			return
		}

	}

	// creates clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %s\n", au.Bold(au.Red("Error")), err)
		return
	}

	realMain(clientset)
}

func realMain(clientset kubernetes.Interface) {
	var mutex = &sync.Mutex{}

	podListWatcher := cache.NewListWatchFromClient(clientset.CoreV1().RESTClient(), "Pods", "", fields.Everything())

	queue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())

	indexer, informer := cache.NewIndexerInformer(podListWatcher, &v1.Pod{}, 0, cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			key, err := cache.MetaNamespaceKeyFunc(obj)
			if err == nil {
				queue.Add(key)
			}
		},
		UpdateFunc: func(old interface{}, new interface{}) {
			key, err := cache.MetaNamespaceKeyFunc(new)
			if err == nil {
				queue.Add(key)
			}
		},
		DeleteFunc: func(obj interface{}) {
			key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
			if err == nil {
				queue.Add(key)
			}
		},
	}, cache.Indexers{})

	controller := NewController(queue, indexer, informer, clientset, mutex, State{})

	stop := make(chan struct{})
	defer close(stop)
	go controller.Run(1, stop)

	select {}
}
