package sectorbuilder

import (
	"encoding/binary"
	"io"
	"os"
	"sort"
	"sync"
	"unsafe"

	sectorbuilder "github.com/filecoin-project/go-sectorbuilder"

	logging "github.com/ipfs/go-log"

	"github.com/filecoin-project/lotus/chain/address"
)

var log = logging.Logger("sectorbuilder")

type SectorSealingStatus = sectorbuilder.SectorSealingStatus

type StagedSectorMetadata = sectorbuilder.StagedSectorMetadata

type SortedSectorInfo = sectorbuilder.SortedSectorInfo

type SectorInfo = sectorbuilder.SectorInfo

const CommLen = sectorbuilder.CommitmentBytesLen

type SectorBuilder struct {
	handle unsafe.Pointer
}

type SectorBuilderConfig struct {
	SectorSize  uint64
	Miner       address.Address
	SealedDir   string
	StagedDir   string
	MetadataDir string
}

func New(cfg *SectorBuilderConfig) (*SectorBuilder, error) {
	proverId := addressToProverID(cfg.Miner)

	sbp, err := sectorbuilder.InitSectorBuilder(cfg.SectorSize, 2, 1, 1, cfg.MetadataDir, proverId, cfg.SealedDir, cfg.StagedDir, 16)
	if err != nil {
		return nil, err
	}

	return &SectorBuilder{
		handle: sbp,
	}, nil
}

func addressToProverID(a address.Address) [32]byte {
	var proverId [32]byte
	copy(proverId[:], a.Payload())
	return proverId
}

func sectorIDtoBytes(sid uint64) [31]byte {
	var out [31]byte
	binary.LittleEndian.PutUint64(out[:], sid)
	return out
}

func (sb *SectorBuilder) Destroy() {
	sectorbuilder.DestroySectorBuilder(sb.handle)
}

func (sb *SectorBuilder) AddPiece(pieceKey string, pieceSize uint64, file io.Reader) (uint64, error) {
	f, werr, err := toReadableFile(file, int64(pieceSize))
	if err != nil {
		return 0, err
	}

	sectorID, err := sectorbuilder.AddPieceFromFile(sb.handle, pieceKey, pieceSize, f)
	if err != nil {
		return 0, err
	}

	return sectorID, werr()
}

// TODO: should *really really* return an io.ReadCloser
func (sb *SectorBuilder) ReadPieceFromSealedSector(pieceKey string) ([]byte, error) {
	return sectorbuilder.ReadPieceFromSealedSector(sb.handle, pieceKey)
}

func (sb *SectorBuilder) SealAllStagedSectors() error {
	panic("dont call this")
	_, err := sectorbuilder.SealAllStagedSectors(sb.handle, sectorbuilder.SealTicket{})
	return err
}

func (sb *SectorBuilder) SealStatus(sector uint64) (SectorSealingStatus, error) {
	return sectorbuilder.GetSectorSealingStatusByID(sb.handle, sector)
}

func (sb *SectorBuilder) GetAllStagedSectors() ([]uint64, error) {
	sectors, err := sectorbuilder.GetAllStagedSectors(sb.handle)
	if err != nil {
		return nil, err
	}

	out := make([]uint64, len(sectors))
	for i, v := range sectors {
		out[i] = v.SectorID
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i] < out[j]
	})

	return out, nil
}

func (sb *SectorBuilder) GeneratePoSt(sectorInfo SortedSectorInfo, challengeSeed [CommLen]byte, faults []uint64) ([]byte, error) {
	// Wait, this is a blocking method with no way of interrupting it?
	// does it checkpoint itself?
	return sectorbuilder.GeneratePoSt(sb.handle, sectorInfo, challengeSeed, faults)
}

var UserBytesForSectorSize = sectorbuilder.GetMaxUserBytesPerStagedSector

func VerifySeal(sectorSize uint64, commR, commD []byte, proverID address.Address, ticket []byte, sectorID uint64, proof []byte) (bool, error) {
	var commRa, commDa, ticketa [32]byte
	copy(commRa[:], commR)
	copy(commDa[:], commD)
	copy(ticketa[:], ticket)
	proverIDa := addressToProverID(proverID)

	return sectorbuilder.VerifySeal(sectorSize, commRa, commDa, proverIDa, ticketa, sectorID, proof)
}

func VerifyPieceInclusionProof(sectorSize uint64, pieceSize uint64, commP []byte, commD []byte, proof []byte) (bool, error) {
	var commPa, commDa [32]byte
	copy(commPa[:], commP)
	copy(commDa[:], commD)

	return sectorbuilder.VerifyPieceInclusionProof(sectorSize, pieceSize, commPa, commDa, proof)
}

func NewSortedSectorInfo(sectors []SectorInfo) SortedSectorInfo {
	return sectorbuilder.NewSortedSectorInfo(sectors...)
}

func VerifyPost(sectorSize uint64, sectorInfo SortedSectorInfo, challengeSeed [CommLen]byte, proof []byte, faults []uint64) (bool, error) {
	return sectorbuilder.VerifyPoSt(sectorSize, sectorInfo, challengeSeed, proof, faults)
}

func GeneratePieceCommitment(piece io.Reader, pieceSize uint64) (commP [CommLen]byte, err error) {
	f, werr, err := toReadableFile(piece, int64(pieceSize))
	if err != nil {
		return [32]byte{}, err
	}

	commP, err = sectorbuilder.GeneratePieceCommitmentFromFile(f, pieceSize)
	if err != nil {
		return [32]byte{}, err
	}

	return commP, werr()
}

func toReadableFile(r io.Reader, n int64) (*os.File, func() error, error) {
	f, ok := r.(*os.File)
	if ok {
		return f, func() error { return nil }, nil
	}

	var w *os.File

	f, w, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}

	var wait sync.Mutex
	var werr error

	wait.Lock()
	go func() {
		defer wait.Unlock()

		_, werr = io.CopyN(w, r, n)

		err := w.Close()
		if werr == nil {
			werr = err
		}
	}()

	return f, func() error {
		wait.Lock()
		return werr
	}, nil
}
