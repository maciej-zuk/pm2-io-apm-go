package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	pm2io "github.com/keymetrics/pm2-io-apm-go"
	"github.com/keymetrics/pm2-io-apm-go/services"
	"github.com/keymetrics/pm2-io-apm-go/structures"
)

func main() {
	Pm2Io := pm2io.Pm2Io{
		Config: &structures.Config{
			NonStandalone: true,
			Name:          "Golang App",
		},
	}
	err := Pm2Io.Start()
	if err != nil {
		panic(err)
	}

	metric := structures.CreateMetric("test", "metric", "unit")
	services.AddMetric(&metric)

	nbreq := structures.Metric{
		Name:  "nbreq",
		Value: 0,
	}
	services.AddMetric(&nbreq)

	services.AddAction(&structures.Action{
		ActionName: "Test",
		Callback: func(_ map[string]interface{}) string {
			log.Println("Action TEST")
			return "I am the test answer"
		},
	})

	services.AddAction(&structures.Action{
		ActionName: "Tric",
	})

	services.AddAction(&structures.Action{
		ActionName: "Get env",
		Callback: func(_ map[string]interface{}) string {
			return strings.Join(os.Environ(), "\n")
		},
	})

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 1000; i++ {
			fmt.Fprintf(w, "Hello")
		}
		nbreq.Value++
	})

	go func() {
		ticker := time.NewTicker(2 * time.Second)
		log.Println("created 2s ticker")
		for {
			<-ticker.C
			metric.Value++
			//cause := errors.New("Niaha")
			//err := errors.WithStack(cause)
			//Pm2Io.Notifier.Error(err)
		}
	}()

	go func() {
		ticker := time.NewTicker(4 * time.Second)
		log.Println("created log ticker")
		for {
			<-ticker.C
			Pm2Io.Notifier.Log("I love logging things\n")
		}
	}()

	/*go func() {
		ticker := time.NewTicker(10 * time.Second)
		log.Println("created reset ticker")
		for {
			<-ticker.C
			log.Println("RestartTransporter")
			Pm2Io.RestartTransporter()
		}
	}()*/

	/*go func() {
		ticker := time.NewTicker(6 * time.Second)
		log.Println("created log ticker")
		for {
			<-ticker.C
			cause := errors.New("Fatal panic error")
			err := errors.WithStack(cause)
			Pm2Io.Panic(err)
		}
	}()*/

	http.ListenAndServe(":8089", nil)
}
