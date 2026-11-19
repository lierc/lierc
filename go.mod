module github.com/lierc/lierc

go 1.27

require (
	github.com/VictoriaMetrics/metrics v1.44.1
	github.com/ergochat/irc-go v0.7.0
	github.com/nsqio/go-nsq v1.1.0
	golang.org/x/time v0.16.0
)

require (
	github.com/golang/snappy v1.0.0 // indirect
	github.com/valyala/fastrand v1.1.0 // indirect
	github.com/valyala/histogram v1.2.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
)

replace github.com/ergochat/irc-go => github.com/lierc/irc-go v0.7.1-lierc.1
