// +build !nodaemon

package main

import (
	"context"

	"github.com/filecoin-project/go-lotus/api"
	"github.com/multiformats/go-multiaddr"
	"gopkg.in/urfave/cli.v2"

	"github.com/filecoin-project/go-lotus/node"
	"github.com/filecoin-project/go-lotus/node/repo"
)

// DaemonCmd is the `go-lotus daemon` command
var DaemonCmd = &cli.Command{
	Name:  "daemon",
	Usage: "Start a lotus daemon process",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:  "api",
			Value: "1234",
		},
	},
	Action: func(cctx *cli.Context) error {
		ctx := context.Background()
		r, err := repo.NewFS(cctx.String("repo"))
		if err != nil {
			return err
		}

		if err := r.Init(); err != nil && err != repo.ErrRepoExists {
			return err
		}

		var api api.FullNode
		err = node.New(ctx,
			node.FullAPI(&api),

			node.Online(),
			node.Repo(r),

			node.Override(node.SetApiEndpointKey, func(lr repo.LockedRepo) error {
				apima, err := multiaddr.NewMultiaddr("/ip4/127.0.0.1/tcp/" + cctx.String("api"))
				if err != nil {
					return err
				}
				return lr.SetAPIEndpoint(apima)
			}),
		)
		if err != nil {
			return err
		}

		// TODO: properly parse api endpoint (or make it a URL)
		return serveRPC(api, "127.0.0.1:"+cctx.String("api"))
	},
}