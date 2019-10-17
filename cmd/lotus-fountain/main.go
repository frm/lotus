package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	rice "github.com/GeertJohan/go.rice"
	logging "github.com/ipfs/go-log"
	peer "github.com/libp2p/go-libp2p-peer"
	"golang.org/x/xerrors"
	"gopkg.in/urfave/cli.v2"

	"github.com/filecoin-project/go-lotus/api"
	"github.com/filecoin-project/go-lotus/build"
	"github.com/filecoin-project/go-lotus/chain/actors"
	"github.com/filecoin-project/go-lotus/chain/address"
	"github.com/filecoin-project/go-lotus/chain/types"
	lcli "github.com/filecoin-project/go-lotus/cli"
)

var log = logging.Logger("main")

var sendPerRequest = types.NewInt(500_000_000)

func main() {
	logging.SetLogLevel("*", "INFO")

	log.Info("Starting fountain")

	local := []*cli.Command{
		runCmd,
	}

	app := &cli.App{
		Name:    "lotus-fountain",
		Usage:   "Devnet token distribution utility",
		Version: build.Version,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "repo",
				EnvVars: []string{"LOTUS_PATH"},
				Value:   "~/.lotus", // TODO: Consider XDG_DATA_HOME
			},
		},

		Commands: local,
	}

	if err := app.Run(os.Args); err != nil {
		log.Warn(err)
		return
	}
}

var runCmd = &cli.Command{
	Name:  "run",
	Usage: "Start lotus fountain",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:  "front",
			Value: "127.0.0.1:7777",
		},
		&cli.StringFlag{
			Name: "from",
		},
	},
	Action: func(cctx *cli.Context) error {
		nodeApi, closer, err := lcli.GetFullNodeAPI(cctx)
		if err != nil {
			return err
		}
		defer closer()
		ctx := lcli.ReqContext(cctx)

		v, err := nodeApi.Version(ctx)
		if err != nil {
			return err
		}

		log.Info("Remote version: %s", v.Version)

		from, err := address.NewFromString(cctx.String("from"))
		if err != nil {
			return xerrors.Errorf("parsing source address (provide correct --from flag!): %w", err)
		}

		h := &handler{
			ctx:  ctx,
			api:  nodeApi,
			from: from,
			limiter: NewLimiter(LimiterConfig{
				TotalRate:   time.Second,
				TotalBurst:  20,
				IPRate:      5 * time.Minute,
				IPBurst:     5,
				WalletRate:  time.Hour,
				WalletBurst: 1,
			}),
			colLimiter: NewLimiter(LimiterConfig{
				TotalRate:   time.Second,
				TotalBurst:  20,
				IPRate:      24 * time.Hour,
				IPBurst:     1,
				WalletRate:  24 * 364 * time.Hour,
				WalletBurst: 1,
			}),
		}

		http.Handle("/", http.FileServer(rice.MustFindBox("site").HTTPBox()))
		http.HandleFunc("/send", h.send)
		http.HandleFunc("/mkminer", h.mkminer)

		fmt.Printf("Open http://%s\n", cctx.String("front"))

		go func() {
			<-ctx.Done()
			os.Exit(0)
		}()

		return http.ListenAndServe(cctx.String("front"), nil)
	},
}

type handler struct {
	ctx context.Context
	api api.FullNode

	from address.Address

	limiter    *Limiter
	colLimiter *Limiter
}

func (h *handler) send(w http.ResponseWriter, r *http.Request) {
	// General limiter to allow throttling all messages that can make it into the mpool
	if !h.limiter.Allow() {
		http.Error(w, http.StatusText(http.StatusTooManyRequests), http.StatusTooManyRequests)
		return
	}

	// Limit based on IP
	limiter := h.limiter.GetIPLimiter(r.RemoteAddr)
	if !limiter.Allow() {
		http.Error(w, http.StatusText(http.StatusTooManyRequests), http.StatusTooManyRequests)
		return
	}

	to, err := address.NewFromString(r.FormValue("address"))
	if err != nil {
		w.WriteHeader(400)
		w.Write([]byte(err.Error()))
		return
	}

	// Limit based on wallet address
	limiter = h.limiter.GetWalletLimiter(to.String())
	if !limiter.Allow() {
		http.Error(w, http.StatusText(http.StatusTooManyRequests), http.StatusTooManyRequests)
		return
	}

	smsg, err := h.api.MpoolPushMessage(h.ctx, &types.Message{
		Value: sendPerRequest,
		From:  h.from,
		To:    to,

		GasPrice: types.NewInt(0),
		GasLimit: types.NewInt(1000),
	})
	if err != nil {
		w.WriteHeader(400)
		w.Write([]byte(err.Error()))
		return
	}

	w.Write([]byte(smsg.Cid().String()))
}

func (h *handler) mkminer(w http.ResponseWriter, r *http.Request) {
	// General limiter owner allow throttling all messages that can make it into the mpool
	if !h.colLimiter.Allow() {
		http.Error(w, http.StatusText(http.StatusTooManyRequests), http.StatusTooManyRequests)
		return
	}

	// Limit based on IP
	limiter := h.colLimiter.GetIPLimiter(r.RemoteAddr)
	if !limiter.Allow() {
		http.Error(w, http.StatusText(http.StatusTooManyRequests), http.StatusTooManyRequests)
		return
	}

	owner, err := address.NewFromString(r.FormValue("address"))
	if err != nil {
		w.WriteHeader(400)
		w.Write([]byte(err.Error()))
		return
	}

	if owner.Protocol() != address.BLS {
		w.WriteHeader(400)
		w.Write([]byte("Miner address must use BLS"))
		return
	}

	log.Infof("mkactoer on %s", owner)

	// Limit based on wallet address
	limiter = h.colLimiter.GetWalletLimiter(owner.String())
	if !limiter.Allow() {
		http.Error(w, http.StatusText(http.StatusTooManyRequests), http.StatusTooManyRequests)
		return
	}
	collateral, err := h.api.StatePledgeCollateral(r.Context(), nil)
	if err != nil {
		w.WriteHeader(400)
		w.Write([]byte(err.Error()))
		return
	}

	smsg, err := h.api.MpoolPushMessage(h.ctx, &types.Message{
		Value: sendPerRequest,
		From:  h.from,
		To:    owner,

		GasPrice: types.NewInt(0),
		GasLimit: types.NewInt(1000),
	})
	if err != nil {
		w.WriteHeader(400)
		w.Write([]byte("pushfunds: " + err.Error()))
		return
	}
	log.Infof("push funds to %s: %s", owner, smsg.Cid())

	mw, err := h.api.StateWaitMsg(r.Context(), smsg.Cid())
	if err != nil {
		w.WriteHeader(400)
		w.Write([]byte(err.Error()))
		return
	}

	if mw.Receipt.ExitCode != 0 {
		w.WriteHeader(400)
		w.Write([]byte(xerrors.Errorf("create storage miner failed: exit code %d", mw.Receipt.ExitCode).Error()))
		return
	}

	log.Infof("sendto %s ok", owner)

	params, err := actors.SerializeParams(&actors.CreateStorageMinerParams{
		Owner:      owner,
		Worker:     owner,
		SectorSize: types.NewInt(build.SectorSize),
		PeerID:     peer.ID("SETME"),
	})
	if err != nil {
		w.WriteHeader(400)
		w.Write([]byte(err.Error()))
		return
	}

	createStorageMinerMsg := &types.Message{
		To:    actors.StorageMarketAddress,
		From:  h.from,
		Value: collateral,

		Method: actors.SPAMethods.CreateStorageMiner,
		Params: params,

		GasLimit: types.NewInt(10000000),
		GasPrice: types.NewInt(0),
	}

	signed, err := h.api.MpoolPushMessage(r.Context(), createStorageMinerMsg)
	if err != nil {
		w.WriteHeader(400)
		w.Write([]byte(err.Error()))
		return
	}

	log.Infof("smc %s", owner)

	mw, err = h.api.StateWaitMsg(r.Context(), signed.Cid())
	if err != nil {
		w.WriteHeader(400)
		w.Write([]byte(err.Error()))
		return
	}

	if mw.Receipt.ExitCode != 0 {
		w.WriteHeader(400)
		w.Write([]byte(xerrors.Errorf("create storage miner failed: exit code %d", mw.Receipt.ExitCode).Error()))
		return
	}

	addr, err := address.NewFromBytes(mw.Receipt.Return)
	if err != nil {
		w.WriteHeader(400)
		w.Write([]byte(err.Error()))
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(200)
	fmt.Fprintf(w, "New storage miners address is: %s\n", addr)
	fmt.Fprintf(w, "Run lotus-storage-miner init --actor=%s -owner=%s", addr, owner)
}
