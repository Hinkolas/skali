package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/Hinkolas/skali/internal/cluster"
	"github.com/Hinkolas/skali/internal/config"
)

// runEnroll joins this machine to a cluster as a worker node. It loads only
// config.Agent: enrollment happens on the new node, which has no database and
// no serve-only settings — its state is the identity written to DATA_DIR.
func runEnroll(args []string) error {
	fs := flag.NewFlagSet("enroll", flag.ContinueOnError)
	master := fs.String("master", "", "master cluster address as host:port (required)")
	token := fs.String("token", "", "one-time join token minted on the master (required)")
	advertise := fs.String("advertise-addr", "", "host:port the master dials back for this node (default: auto-detect)")
	dataDir := fs.String("data-dir", "", "directory for the node identity (default: $DATA_DIR or /var/lib/skalid)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *master == "" || *token == "" {
		return errors.New("usage: skalid enroll --master <host:port> --token <join-token> [--advertise-addr <host:port>] [--data-dir <dir>]")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg, err := config.Load[config.Agent](ctx)
	if err != nil {
		return err
	}
	if *dataDir == "" {
		*dataDir = cfg.DataDir
	}

	identity, roles, err := cluster.RunEnroll(ctx, cluster.EnrollOptions{
		MasterAddr:    *master,
		Token:         *token,
		AdvertiseAddr: *advertise,
		GRPCAddr:      cfg.GRPCAddr,
		DataDir:       *dataDir,
	})
	if err != nil {
		return err
	}

	fmt.Printf("enrolled as node %s\n", identity.NodeID)
	fmt.Printf("  roles:          %s\n", strings.Join(roles, ", "))
	fmt.Printf("  advertise addr: %s\n", identity.AdvertiseAddr)
	fmt.Printf("  identity dir:   %s\n", *dataDir)
	if identity.RegistryAddr != "" {
		fmt.Printf("  image mirror:   %s (docker trust installed)\n", identity.RegistryAddr)
	}
	fmt.Println("start the node with: skalid agent")
	return nil
}
