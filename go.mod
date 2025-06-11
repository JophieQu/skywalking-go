module github.com/apache/skywalking-go

go 1.24

require (
	github.com/google/uuid v1.6.0
	github.com/pkg/errors v0.9.1
	google.golang.org/grpc v1.72.0
	skywalking.apache.org/repo/goapi v0.0.0-20230314034821-0c5a44bb767a
)

require (
	github.com/cncf/xds/go v0.0.0-20250121191232-2f005788dc42 // indirect
	github.com/envoyproxy/protoc-gen-validate v1.2.1 // indirect
	golang.org/x/net v0.40.0 // indirect
	golang.org/x/sys v0.33.0 // indirect
	golang.org/x/text v0.25.0 // indirect
	google.golang.org/genproto v0.0.0-20230410155749-daa745c078e1 // indirect
	google.golang.org/protobuf v1.36.6 // indirect
)

replace skywalking.apache.org/repo/goapi v0.0.0-20230314034821-0c5a44bb767a => github.com/JophieQu/skywalking-goapi v0.0.0-20250611102716-79730d0441cb
