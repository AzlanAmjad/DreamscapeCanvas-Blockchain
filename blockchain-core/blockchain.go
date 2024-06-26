package core

import (
	"bytes"
	"fmt"
	"os"
	"sync"

	crypto "github.com/AzlanAmjad/DreamscapeCanvas-Blockchain/cryptography"
	types "github.com/AzlanAmjad/DreamscapeCanvas-Blockchain/data-types"
	"github.com/go-kit/log"
)

const BLOCK_MINING_DIFFICULTY = 2 // indicates how many leading zeros the block hash should have

type Blockchain struct {
	ID string
	// should never be used outside a blockchain function
	// (i.e, should never be used as Blockchain.Lock outside a blockchain function)
	Lock                sync.RWMutex
	BlockHeaders        []*BlockHeader
	BlockHeadersHashMap map[types.Hash]*BlockHeader
	Storage             Storage
	AccountStates       *AccountStates

	// temporary blockchain state, later should be persisted on disk
	CollectionState map[types.Hash]*CollectionTransaction
	MintState       map[types.Hash]*MintTransaction

	Validator Validator
	// block encoder and decoder
	BlockEncoder Encoder[*Block]
	BlockDecoder Decoder[*Block]
	// block header hasher
	BlockHeaderHasher Hasher[*BlockHeader]
	Logger            log.Logger
	// mempool for transactions
	memPool *TxPool
}

// NewBlockchain creates a new empty blockchain. With the default block validator.
func NewBlockchain(storage Storage, genesis *Block, ID string, mempool *TxPool) (*Blockchain, error) {

	as := NewAccountStates() // TODO: persist on disk and load from disk on startup

	bc := &Blockchain{
		BlockHeaders:        make([]*BlockHeader, 0),
		BlockHeadersHashMap: make(map[types.Hash]*BlockHeader),
		CollectionState:     make(map[types.Hash]*CollectionTransaction),
		MintState:           make(map[types.Hash]*MintTransaction),
		Storage:             storage,
		AccountStates:       as, // TODO: persist on disk and load from disk on startup
		ID:                  ID,
		memPool:             mempool,
	}

	// add default block encoder and decoder
	bc.BlockEncoder = NewBlockEncoder()
	bc.BlockDecoder = NewBlockDecoder()
	// add default block header hasher
	bc.BlockHeaderHasher = NewBlockHeaderHasher()
	// set the default block validator
	bc.SetValidator(NewBlockValidator(bc))
	// set the default logger
	bc.Logger = log.NewLogfmtLogger(os.Stderr)
	bc.Logger = log.With(bc.Logger, "ID", bc.ID)

	// add the genesis block to the blockchain
	err := bc.addBlockWithoutValidation(genesis)

	// log the creation of the blockchain
	bc.Logger.Log(
		"msg", "blockchain created",
		"blockchain_id", bc.ID,
		"genesis_block_has h", genesis.GetHash(bc.BlockHeaderHasher),
	)

	return bc, err
}

// addBlockWithoutValidation adds a block to the blockchain without validation.
func (bc *Blockchain) addBlockWithoutValidation(block *Block) error {
	bc.Lock.Lock()
	// process the transactions in the block
	// processing transactions helps us update the state of the node, which
	// aims to be consistent with the state of the blockchain network
	for _, tx := range block.Transactions {
		switch tx.Type {
		case TxCommon:
			fmt.Println("Common transaction")
		case TxCollection:
			err := bc.HandleCollectionTransaction(tx, block)
			if err != nil {
				bc.Logger.Log("msg", "Error handling collection transaction", "error", err.Error())
				continue
			}
		case TxMint:
			err := bc.HandleMintTransaction(tx, block)
			if err != nil {
				bc.Logger.Log("msg", "Error handling mint transaction", "error", err.Error())
				continue
			}
		case TxCryptoTransfer:
			err := bc.HandleCryptoTransferTransaction(tx, block)
			if err != nil {
				bc.Logger.Log("msg", "Error handling crypto transfer transaction", "error", err.Error())
				continue
			}
		default:
			fmt.Println("Unknown transaction type")
		}
		// remove this transaction from the mempool if it exists
		if bc.memPool != nil {
			bc.memPool.RemoveFromPending(tx)
		}
	}

	// add the block to the storage
	err := bc.Storage.Put(block, bc.BlockEncoder)
	if err != nil {
		return err
	}
	// add the block to the blockchain headers
	bc.BlockHeaders = append(bc.BlockHeaders, block.Header)
	bc.BlockHeadersHashMap[block.GetHash(bc.BlockHeaderHasher)] = block.Header
	bc.Lock.Unlock()

	bc.Logger.Log(
		"msg", "Block added to the blockchain",
		"block_index", block.Header.Index,
		"block_hash", block.GetHash(bc.BlockHeaderHasher),
		"data_hash", block.Header.DataHash,
		"transactions", len(block.Transactions),
		"blockchain_height", bc.GetHeight(),
	)

	return err
}

func (bc *Blockchain) HandleCollectionTransaction(tx *Transaction, block *Block) error {
	// decode the transaction
	collection_tx := &CollectionTransaction{}
	err := collection_tx.Decode(bytes.NewReader(tx.Data), NewCollectionTransactionDecoder())
	if err != nil {
		bc.Logger.Log("msg", "Error decoding collection transaction", "error", err.Error())
		return fmt.Errorf("error decoding collection transaction")
	}

	// add the collection transaction to the blockchain state
	bc.CollectionState[tx.GetHash(block.TransactionHasher)] = collection_tx
	bc.Logger.Log("msg", "Collection transaction processed", "collection_name", collection_tx.Name)

	return nil
}

func (bc *Blockchain) HandleMintTransaction(tx *Transaction, block *Block) error {
	// decode the transaction
	mint_tx := &MintTransaction{}
	err := mint_tx.Decode(bytes.NewReader(tx.Data), NewMintTransactionDecoder())
	if err != nil {
		bc.Logger.Log("msg", "Error decoding mint transaction", "error", err.Error())
		return fmt.Errorf("error decoding mint transaction")
	}

	// check if the mint transaction is valid
	_, ok := bc.CollectionState[mint_tx.Collection]
	if !ok {
		bc.Logger.Log("msg", "Collection not found", "collection_hash", mint_tx.Collection)
		return fmt.Errorf("collection not found")
	}

	// verify the signature on the transaction
	valid, err := mint_tx.VerifySignature()
	if err != nil {
		bc.Logger.Log("msg", "Error verifying mint transaction signature", "error", err.Error())
		return fmt.Errorf("error verifying mint transaction signature")
	}
	if !valid {
		bc.Logger.Log("msg", "Invalid mint transaction signature")
		return fmt.Errorf("invalid mint transaction signature")
	}

	// add the mint transaction to the blockchain state
	bc.MintState[tx.GetHash(block.TransactionHasher)] = mint_tx
	bc.Logger.Log("msg", "Mint transaction processed", "collection_hash", mint_tx.Collection, "nft_hash", mint_tx.NFT)

	return nil
}

func (bc *Blockchain) HandleCryptoTransferTransaction(tx *Transaction, block *Block) error {
	// decode the transaction
	crypto_transfer_tx := &CryptoTransferTransaction{}
	err := crypto_transfer_tx.Decode(bytes.NewReader(tx.Data), NewCryptoTransferTransactionDecoder())
	if err != nil {
		bc.Logger.Log("msg", "Error decoding crypto transfer transaction", "error", err.Error())
		return fmt.Errorf("error decoding crypto transfer transaction")
	}

	// if this is the coinbase account sending to itself, we will just add the amount to the account
	// this means that the coinbase account is making coins out of thin air / increasing the supply
	pub_key, _ := GetCoinbaseAccount()
	if tx.From.GetAddress() == pub_key.GetAddress() && crypto_transfer_tx.To.GetAddress() == pub_key.GetAddress() {
		bc.AccountStates.AddBalance(tx.From.GetAddress(), crypto_transfer_tx.Amount)
		bc.Logger.Log("msg", "Coinbase account minted coins", "amount", crypto_transfer_tx.Amount)
	} else {
		// transfer the amount from the sender to the receiver
		err = bc.AccountStates.Transfer(tx.From.GetAddress(), crypto_transfer_tx.To.GetAddress(), crypto_transfer_tx.Amount)
		if err != nil {
			bc.Logger.Log("msg", "Error transferring amount", "error", err.Error())
			return fmt.Errorf("error transferring amount")
		}
	}

	bc.Logger.Log("msg", "Crypto transfer transaction processed", "from", tx.From.String(), "to", crypto_transfer_tx.To.String(), "amount", crypto_transfer_tx.Amount)

	from_balance, err := bc.AccountStates.GetBalance(tx.From.GetAddress())
	if err != nil {
		bc.Logger.Log("msg", "Error getting balance", "error", err.Error())
		return fmt.Errorf("error getting balance")
	}
	to_balance, err := bc.AccountStates.GetBalance(crypto_transfer_tx.To.GetAddress())
	if err != nil {
		bc.Logger.Log("msg", "Error getting balance", "error", err.Error())
		return fmt.Errorf("error getting balance")
	}
	bc.Logger.Log("from balance", from_balance, "to balance", to_balance)

	return nil
}

// SetValidator sets the validator of the blockchain.
func (bc *Blockchain) SetValidator(validator Validator) {
	bc.Validator = validator
}

// [0, 1, 2, 3] -> len = 4
// [0, 1, 2, 3] -> height = 3
// GetHeight returns the largest index of the blockchain.
func (bc *Blockchain) GetHeight() uint32 {
	bc.Lock.RLock()
	defer bc.Lock.RUnlock()

	return uint32(len(bc.BlockHeaders) - 1)
}

// AddBlock adds a block to the blockchain.
// int return value is only for validate block error code, every other error will have a 0 error code
func (bc *Blockchain) AddBlock(block *Block) (int, error) {
	if bc.Validator == nil {
		return 0, fmt.Errorf("no validator to validate the block")
	}

	// validate the block before adding it to the blockchain
	error_code, err := bc.Validator.ValidateBlock(block)
	if err != nil {
		return error_code, err
	}

	// add the block to the blockchain
	err = bc.addBlockWithoutValidation(block)
	if err != nil {
		return 0, err
	}

	return 0, nil
}

// get block by index
func (bc *Blockchain) GetBlockByIndex(index uint32) (*Block, error) {
	if index > bc.GetHeight() {
		return nil, fmt.Errorf("block index is invalid, block index: %d, blockchain height: %d", index, bc.GetHeight())
	}

	bc.Lock.RLock()
	defer bc.Lock.RUnlock()
	return bc.Storage.Get(index, bc.BlockDecoder)
}

// get block by hash
func (bc *Blockchain) GetBlockByHash(hash types.Hash) (*Block, error) {
	bc.Lock.RLock()
	defer bc.Lock.RUnlock()
	header, ok := bc.BlockHeadersHashMap[hash]
	if !ok {
		return nil, fmt.Errorf("block hash is not found in the blockchain")
	}
	return bc.Storage.Get(header.Index, bc.BlockDecoder)
}

// get multiple blocks in a specified range
// start is inclusive
// end is not inclusive
func (bc *Blockchain) GetBlocks(start, end uint32) ([]*Block, error) {
	if start >= end {
		return nil, fmt.Errorf("start index is greater than or equal to end index")
	}

	bc.Lock.RLock() // lock because we are reading
	defer bc.Lock.RUnlock()
	return bc.Storage.GetBlocks(start, end, bc.BlockDecoder)
}

// HasBlock function compares the index of the block with the height of the blockchain.
func (bc *Blockchain) HasBlock(block *Block) bool {
	return block.Header.Index <= bc.GetHeight()
}

// GetHeaderByIndex returns header by block index
func (bc *Blockchain) GetHeaderByIndex(index uint32) (*BlockHeader, error) {
	if index > bc.GetHeight() {
		return nil, fmt.Errorf("block index is invalid, block index: %d, blockchain height: %d", index, bc.GetHeight())
	}

	bc.Lock.RLock()
	defer bc.Lock.RUnlock()

	return bc.BlockHeaders[index], nil
}

func (bc *Blockchain) GetBlockHash(index uint32) (types.Hash, error) {
	header, err := bc.GetHeaderByIndex(index)
	if err != nil {
		return types.Hash{}, err
	}
	return bc.BlockHeaderHasher.Hash(header), nil
}

// get transaction by hash using the transaction mempool
func (bc *Blockchain) GetTransactionByHash(hash types.Hash) (*Transaction, error) {
	if bc.memPool == nil {
		return nil, fmt.Errorf("blockchain mempool is nil")
	}
	tx, err := bc.memPool.GetTransactionByHash(hash)
	if err != nil {
		return nil, fmt.Errorf("transaction not found in mempool")
	}
	return tx, nil
}

func (bc *Blockchain) IsBlockHashValid(hash types.Hash) bool {
	// check if the hash has BLOCK_MINING_DIFFICULTY leading zeros
	for i := 0; i < BLOCK_MINING_DIFFICULTY; i++ {
		if hash[i] != 0 {
			return false
		}
	}
	return true
}

var coinbase_account_pub *crypto.PublicKey
var coinbase_account_priv *crypto.PrivateKey

// generate a coinbase account for this blockchain
func GetCoinbaseAccount() (*crypto.PublicKey, *crypto.PrivateKey) {
	// generate a static public key for the coinbase account
	// this key will be used to mint the initial coins
	// this key generation is done on another elliptic curve

	// check if coinbase account is already generated
	if coinbase_account_priv != nil && coinbase_account_pub != nil {
		return coinbase_account_pub, coinbase_account_priv
	}

	// generate a new private key
	privateKey := crypto.GeneratePrivateKey()
	// generate the public key from the private key
	publicKey := privateKey.GetPublicKey()

	return &publicKey, &privateKey
}
