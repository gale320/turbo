package turbo

import (
	"context"
	"fmt"
	"git.apache.org/thrift.git/lib/go/thrift"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

var httpServerQuit = make(chan bool)
var serviceQuit = make(chan bool)

var waitOnList []chan bool = make([]chan bool, 2)

var reloadConfig = make(chan bool)

func waitForQuit() {
	<-httpServerQuit
	<-serviceQuit
	//for _, c := range waitOnList {
	// error:
	// transport: http2Server.HandleStreams failed to read frame:
	// read tcp 127.0.0.1:50051->127.0.0.1:55313: use of closed network connection
	//	fmt.Printf(strconv.FormatBool(<-c))
	//}
}

type grpcClientCreator func(conn *grpc.ClientConn) interface{}

// StartGRPC starts both HTTP server and GRPC service
func StartGRPC(pkgPath, configFileName string, servicePort int, clientCreator grpcClientCreator, s switcher, registerServer func(s *grpc.Server)) {
	fmt.Println("Starting Turbo...")
	LoadServiceConfig("grpc", pkgPath, configFileName)
	go startGrpcServiceInternal(servicePort, registerServer, false)
	go startGrpcHTTPServerInternal(clientCreator, s)
	waitForQuit()
	fmt.Println("Turbo exit, bye!")
}

// StartGrpcHTTPServer starts a HTTP server which sends requests via grpc
func StartGrpcHTTPServer(pkgPath, configFileName string, clientCreator grpcClientCreator, s switcher) {
	LoadServiceConfig("grpc", pkgPath, configFileName)
	startGrpcHTTPServerInternal(clientCreator, s)
}

func startGrpcHTTPServerInternal(clientCreator grpcClientCreator, s switcher) {
	fmt.Println("Starting HTTP Server...")
	switcherFunc = s
	err := initGrpcService(clientCreator)
	if err != nil {
		fmt.Println(err.Error())
		os.Exit(1)
	}
	defer closeGrpcService()
	startHTTPServer(Config.HTTPPortStr(), router())
}

type thriftClientCreator func(trans thrift.TTransport, f thrift.TProtocolFactory) interface{}

var thriftServiceStarted = make(chan bool)

// StartTHRIFT starts both HTTP server and Thrift service
func StartTHRIFT(pkgPath, configFileName string, port int, clientCreator thriftClientCreator, s switcher, registerTProcessor func() thrift.TProcessor) {
	fmt.Println("Starting Turbo...")
	LoadServiceConfig("grpc", pkgPath, configFileName)
	go startThriftServiceInternal(port, registerTProcessor, false)
	<-thriftServiceStarted
	go startThriftHTTPServerInternal(clientCreator, s)
	waitForQuit()
	fmt.Println("Turbo exit, bye!")
}

// StartThriftHTTPServer starts a HTTP server which sends requests via Thrift
func StartThriftHTTPServer(pkgPath, configFileName string, clientCreator thriftClientCreator, s switcher) {
	LoadServiceConfig("thrift", pkgPath, configFileName)
	startThriftHTTPServerInternal(clientCreator, s)
}

func startThriftHTTPServerInternal(clientCreator thriftClientCreator, s switcher) {
	fmt.Println("Starting HTTP Server...")
	switcherFunc = s
	err := initThriftService(clientCreator)
	if err != nil {
		fmt.Println(err.Error())
		os.Exit(1)
	}
	defer closeThriftService()
	startHTTPServer(Config.HTTPPortStr(), router())
}

func startHTTPServer(portStr string, handler http.Handler) {
	s := &http.Server{
		Addr:    portStr,
		Handler: handler,
	}
	go func() {
		if err := s.ListenAndServe(); err != nil {
			log.Printf("HTTP Server failed to serve: %v", err)
		}
	}()
	waitOnList = append(waitOnList, httpServerQuit)
	fmt.Println("HTTP Server started")
	for {
		//wait for exit
		exit := make(chan os.Signal, 1)
		signal.Notify(exit, os.Interrupt, os.Kill, syscall.SIGTERM, syscall.SIGQUIT)
		select {
		case <-exit:
			fmt.Println("Received CTRL-C, HTTP Server is shutting down...")
			shutDownHTTP(s)
			fmt.Println("HTTP Server stopped")
			close(httpServerQuit)
			return
		case <-reloadConfig:
			s.Handler = router()
			fmt.Println("HTTP Server ServeMux reloaded")
		}
	}
}

func shutDownHTTP(s *http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()
	s.Shutdown(ctx)
}

// StartGrpcService starts a GRPC service
func StartGrpcService(port int, registerServer func(s *grpc.Server)) {
	startGrpcServiceInternal(port, registerServer, true)
}

func startGrpcServiceInternal(port int, registerServer func(s *grpc.Server), alone bool) {
	fmt.Println("Starting GRPC Service...")
	portStr := fmt.Sprintf(":%d", port)
	lis, err := net.Listen("tcp", portStr)
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}
	grpcServer := grpc.NewServer()
	registerServer(grpcServer)
	reflection.Register(grpcServer)
	go func() {
		if err := grpcServer.Serve(lis); err != nil {
			log.Printf("GRPC Service failed to serve: %v", err)
		}
	}()
	waitOnList = append(waitOnList, serviceQuit)
	fmt.Println("GRPC Service started")

	if !alone {
		<-httpServerQuit // wait for http server quit
		fmt.Println("Stopping GRPC Service...")
		grpcServer.Stop()
		fmt.Println("GRPC Service stopped")
		close(serviceQuit)
	} else {
		//wait for exit
		exit := make(chan os.Signal, 1)
		signal.Notify(exit, os.Interrupt, os.Kill, syscall.SIGTERM, syscall.SIGQUIT)
		select {
		case <-exit:
			fmt.Println("Received CTRL-C, GRPC Service is stopping...")
			grpcServer.Stop()
			fmt.Println("GRPC Service stopped")
		}
	}
}

// StartThriftService starts a Thrift service
func StartThriftService(port int, registerTProcessor func() thrift.TProcessor) {
	startThriftServiceInternal(port, registerTProcessor, true)
}

func startThriftServiceInternal(port int, registerTProcessor func() thrift.TProcessor, alone bool) {
	fmt.Println("Starting Thrift Service...")
	portStr := fmt.Sprintf(":%d", port)
	transport, err := thrift.NewTServerSocket(portStr)
	if err != nil {
		log.Println("socket error")
		os.Exit(1)
	}
	server := thrift.NewTSimpleServer4(registerTProcessor(), transport,
		thrift.NewTTransportFactory(), thrift.NewTBinaryProtocolFactoryDefault())
	//go server.Serve()
	err = server.Listen()
	if err != nil {
		panic(err)
	}
	go server.AcceptLoop()
	fmt.Println("Thrift Service started")
	thriftServiceStarted <- true

	if !alone {
		<-httpServerQuit // wait for http server quit
		fmt.Println("Stopping Thrift Service...")
		server.Stop()
		fmt.Println("Thrift Service stopped")
		close(serviceQuit)
	} else {
		//wait for exit
		exit := make(chan os.Signal, 1)
		signal.Notify(exit, os.Interrupt, os.Kill, syscall.SIGTERM, syscall.SIGQUIT)
		select {
		case <-exit:
			fmt.Println("Received CTRL-C, Thrift Service is stopping...")
			server.Stop()
			fmt.Println("Thrift Service stopped")
		}
	}
}
