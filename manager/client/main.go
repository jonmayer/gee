package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/user"
	"strings"
	"time"

	"github.com/enfabrica/enkit/flextape/client"
	fpb "github.com/enfabrica/enkit/flextape/proto"
	rpc_license "github.com/enfabrica/enkit/manager/rpc"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	grpcCodes "google.golang.org/grpc/codes"
	grpcStatus "google.golang.org/grpc/status"
)

func run(timeout time.Duration, cmd string, args ...string) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	job := exec.CommandContext(ctx, cmd, args...)
	job.Stdout = os.Stdout
	job.Stderr = os.Stderr

	err := job.Run()
	if ctx.Err() == context.DeadlineExceeded {
		log.Fatalf("Job failed to complete after %s: %s %s\n", timeout, cmd, strings.Join(args, " "))
	}
	if err != nil {
		log.Fatalf("Job \"%s %s\" failed with error %s", cmd, strings.Join(args, " "), err)
	}
	log.Printf("Job completed successfully: %s %s\n", cmd, strings.Join(args, " "))
}

func polling(client rpc_license.LicenseClient, username string, quantity int32, vendor string, feature string,
	timeout time.Duration, cmd string, args ...string) {
	waiting := 0 * time.Second
	interval := 5 * time.Second
	stream, err := client.Polling(context.Background())
	if err != nil {
		log.Fatalf("Failed to get stream object: %s \n", err)
	}
	hash := ""
	acquired := false
	for waiting < timeout {
		request := rpc_license.PollingRequest{Vendor: vendor, Feature: feature, Quantity: quantity, User: username, Hash: hash}
		err := stream.Send(&request)
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Fatalf("Error sending  message: %s \n", err)
		}
		recv, err := stream.Recv()
		if err != nil {
			errStatus, _ := grpcStatus.FromError(err)
			if errStatus.Code() == grpcCodes.InvalidArgument {
				log.Fatalf(errStatus.Message())
			} else if err == io.EOF {
				break
			} else {
				log.Fatalf("Error receiving message: %s \n", err)
			}
		} else {
			hash = recv.Hash
			if recv.Acquired {
				acquired = recv.Acquired
				break
			}
		}
		// no need to continuously log after the initial request
		if waiting == 0 {
			log.Printf("Client %s waiting for %d %s feature %s license \n", hash, quantity, vendor, feature)
		}
		time.Sleep(interval)
		waiting += interval
	}
	if waiting >= timeout {
		log.Fatalf("Failed to acquire %d %s feature %s license after %s\n", quantity, vendor, feature, waiting)
	}
	if acquired {
		log.Printf("Successfully acquired %d %s feature %s license from server\n", quantity, vendor, feature)
		// 12 hours in seconds
		run(60*60*12*time.Second, cmd, args...)
	}
}

func main() {
	var quantity int32 = 1
	host, port := os.Args[1], os.Args[2]
	timeout := flag.Duration("timeout", 7200*time.Second, "Max time waiting in license queue")
	flag.Parse()
	vendor, feature, cmd, args := os.Args[3], os.Args[4], os.Args[5], os.Args[6:]
	user, err := user.Current()
	if err != nil {
		log.Fatalf("Failed to get username: %s \n", err)
	}
	conn, err := grpc.Dial(fmt.Sprintf("%s:%s", host, port), grpc.WithInsecure())
	if err != nil {
		log.Fatalf("Connection failed: %s \n", err)
	}
	defer conn.Close()

	if os.Getenv("LICENSE_SERVER_IMPL") != "flextape" {
		c := rpc_license.NewLicenseClient(conn)
		polling(c, user.Username, quantity, vendor, feature, *timeout, cmd, args...)
	} else {
		id, err := uuid.NewRandom()
		if err != nil {
			log.Fatalf("failed to generate job ID: %w", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), *timeout)
		defer cancel()
		c := client.New(fpb.NewFlextapeClient(conn), user.Username, vendor, feature, id.String())
		err = c.Guard(ctx, cmd, args...)
		if err != nil {
			log.Fatal(err)
		}
	}
}
