package core

type StatusMessage struct {
	ID            string
	CurrentHeight uint32
}

type GetStatusMessage struct {
}

type GetBlocksMessage struct {
	From uint32
	To   uint32
}
