package pkg

import (
	"fmt"
	"os"
)

type logger struct {
}

func (l *logger) Error(message string, args ...interface{}) {
	_, err := fmt.Fprintf(os.Stderr, message+"\n", args...)
	if err != nil {
		panic(err)
	}
}

func (l *logger) Warn(message string, args ...interface{}) {
	_, err := fmt.Fprintf(os.Stderr, message+"\n", args...)
	if err != nil {
		panic(err)
	}
}

func (l *logger) Debug(message string, args ...interface{}) {
	_, err := fmt.Printf(message+"\n", args...)
	if err != nil {
		panic(err)
	}
}

func (l *logger) Trace(message string, args ...interface{}) {
	_, err := fmt.Printf(message+"\n", args...)
	if err != nil {
		panic(err)
	}
}
