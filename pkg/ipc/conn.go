// Copyright 2023 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ipc

import (
	"net"
	"os"
	"path"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/livekit/protocol/logger"
)

const (
	network = "unix"
)

func GetServiceClient(tmpDir string) (IngressServiceClient, error) {
	socketAddr := getServiceSocketAddress(tmpDir)

	conn, err := grpc.NewClient("unix://"+socketAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, err
	}

	grpcClient := NewIngressServiceClient(conn)

	return grpcClient, nil
}

func StartServiceServer(tmpDir string, h IngressServiceServer) (*grpc.Server, error) {
	socketAddr := getServiceSocketAddress(tmpDir)
	os.Remove(socketAddr)

	listener, err := net.Listen(network, socketAddr)
	if err != nil {
		return nil, err
	}

	grpcServer := grpc.NewServer()

	RegisterIngressServiceServer(grpcServer, h)

	go func() {
		err := grpcServer.Serve(listener)
		if err != nil {
			logger.Errorw("failed starting grpc handler", err)
		}
	}()

	return grpcServer, nil
}

type IngressHandlerClientWrapper struct {
	IngressHandlerClient
	conn *grpc.ClientConn
}

func GetHandlerClient(tmpDir string) (*IngressHandlerClientWrapper, error) {
	socketAddr := getHandlerSocketAddress(tmpDir)
	os.Remove(socketAddr)

	conn, err := grpc.NewClient("unix://"+socketAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, err
	}

	grpcClient := NewIngressHandlerClient(conn)

	return &IngressHandlerClientWrapper{
		IngressHandlerClient: grpcClient,
		conn:                 conn,
	}, nil
}

func (ihc *IngressHandlerClientWrapper) Close() error {
	return ihc.conn.Close()
}

func StartHandlerServer(tmpDir string, h IngressHandlerServer) error {
	listener, err := net.Listen(network, getHandlerSocketAddress(tmpDir))
	if err != nil {
		return err
	}

	grpcServer := grpc.NewServer()

	RegisterIngressHandlerServer(grpcServer, h)

	go func() {
		err := grpcServer.Serve(listener)
		if err != nil {
			logger.Errorw("failed starting grpc handler", err)
		}
	}()

	return nil
}

func getServiceSocketAddress(handlerTmpDir string) string {
	return path.Join(handlerTmpDir, "service_ipc.sock")
}

func getHandlerSocketAddress(handlerTmpDir string) string {
	return path.Join(handlerTmpDir, "handler_ipc.sock")
}
