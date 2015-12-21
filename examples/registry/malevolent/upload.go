package main

import (
	"net/http"

	"github.com/Sirupsen/logrus"
)

type uploadChanger struct {
	http.Handler
}

func (u uploadChanger) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	operation := extractOperation(r)
	switch operation {
	case "noupload":
		http.Error(rw, "upload not allowed", 400)
	default:
		logrus.Infof("No upload operation for %q, passing through", operation)
		u.Handler.ServeHTTP(rw, r)
	}
}
