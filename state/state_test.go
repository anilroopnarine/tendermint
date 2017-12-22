package state

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	abci "github.com/tendermint/abci/types"

	crypto "github.com/tendermint/go-crypto"

	cmn "github.com/tendermint/tmlibs/common"
	dbm "github.com/tendermint/tmlibs/db"
	"github.com/tendermint/tmlibs/log"

	cfg "github.com/tendermint/tendermint/config"
	"github.com/tendermint/tendermint/types"
)

// setupTestCase does setup common to all test cases
func setupTestCase(t *testing.T) (func(t *testing.T), dbm.DB, *State) {
	config := cfg.ResetTestRoot("state_")
	stateDB := dbm.NewDB("state", config.DBBackend, config.DBDir())
	state, err := GetState(stateDB, config.GenesisFile())
	assert.NoError(t, err, "expected no error on GetState")
	state.SetLogger(log.TestingLogger())

	tearDown := func(t *testing.T) {}

	return tearDown, stateDB, state
}

// TestStateCopy tests the correct copying behaviour of State.
func TestStateCopy(t *testing.T) {
	tearDown, _, state := setupTestCase(t)
	defer tearDown(t)
	// nolint: vetshadow
	assert := assert.New(t)

	stateCopy := state.Copy()

	assert.True(state.Equals(stateCopy),
		cmn.Fmt(`expected state and its copy to be identical. got %v\n expected %v\n`,
			stateCopy, state))

	stateCopy.LastBlockHeight++
	assert.False(state.Equals(stateCopy), cmn.Fmt(`expected states to be different. got same
        %v`, state))
}

// TestStateSaveLoad tests saving and loading State from a db.
func TestStateSaveLoad(t *testing.T) {
	tearDown, stateDB, state := setupTestCase(t)
	defer tearDown(t)
	// nolint: vetshadow
	assert := assert.New(t)

	state.LastBlockHeight++
	state.Save()

	loadedState := LoadState(stateDB)
	assert.True(state.Equals(loadedState),
		cmn.Fmt(`expected state and its copy to be identical. got %v\n expected %v\n`,
			loadedState, state))
}

// TestABCIResponsesSaveLoad tests saving and loading ABCIResponses.
func TestABCIResponsesSaveLoad(t *testing.T) {
	tearDown, _, state := setupTestCase(t)
	defer tearDown(t)
	// nolint: vetshadow
	assert := assert.New(t)

	state.LastBlockHeight++

	// build mock responses
	block := makeBlock(state, 2)
	abciResponses := NewABCIResponses(block)
	abciResponses.DeliverTx[0] = &abci.ResponseDeliverTx{Data: []byte("foo"), Tags: []*abci.KVPair{}}
	abciResponses.DeliverTx[1] = &abci.ResponseDeliverTx{Data: []byte("bar"), Log: "ok", Tags: []*abci.KVPair{}}
	abciResponses.EndBlock = &abci.ResponseEndBlock{ValidatorUpdates: []*abci.Validator{
		{
			PubKey: crypto.GenPrivKeyEd25519().PubKey().Bytes(),
			Power:  10,
		},
	}}
	abciResponses.txs = nil

	state.SaveABCIResponses(abciResponses)
	loadedAbciResponses := state.LoadABCIResponses()
	assert.Equal(abciResponses, loadedAbciResponses,
		cmn.Fmt(`ABCIResponses don't match: Got %v, Expected %v`, loadedAbciResponses,
			abciResponses))
}

// TestValidatorSimpleSaveLoad tests saving and loading validators.
func TestValidatorSimpleSaveLoad(t *testing.T) {
	tearDown, _, state := setupTestCase(t)
	defer tearDown(t)
	// nolint: vetshadow
	assert := assert.New(t)

	// can't load anything for height 0
	v, err := state.LoadValidators(0)
	assert.IsType(ErrNoValSetForHeight{}, err, "expected err at height 0")

	// should be able to load for height 1
	v, err = state.LoadValidators(1)
	assert.Nil(err, "expected no err at height 1")
	assert.Equal(v.Hash(), state.Validators.Hash(), "expected validator hashes to match")

	// increment height, save; should be able to load for next height
	state.LastBlockHeight++
	state.saveValidatorsInfo()
	v, err = state.LoadValidators(state.LastBlockHeight + 1)
	assert.Nil(err, "expected no err")
	assert.Equal(v.Hash(), state.Validators.Hash(), "expected validator hashes to match")

	// increment height, save; should be able to load for next height
	state.LastBlockHeight += 10
	state.saveValidatorsInfo()
	v, err = state.LoadValidators(state.LastBlockHeight + 1)
	assert.Nil(err, "expected no err")
	assert.Equal(v.Hash(), state.Validators.Hash(), "expected validator hashes to match")

	// should be able to load for next next height
	_, err = state.LoadValidators(state.LastBlockHeight + 2)
	assert.IsType(ErrNoValSetForHeight{}, err, "expected err at unknown height")
}

// TestValidatorChangesSaveLoad tests saving and loading a validator set with changes.
func TestValidatorChangesSaveLoad(t *testing.T) {
	tearDown, _, state := setupTestCase(t)
	defer tearDown(t)
	// nolint: vetshadow
	assert := assert.New(t)

	// change vals at these heights
	changeHeights := []int64{1, 2, 4, 5, 10, 15, 16, 17, 20}
	N := len(changeHeights)

	// each valset is just one validator.
	// create list of them
	pubkeys := make([]crypto.PubKey, N+1)
	_, val := state.Validators.GetByIndex(0)
	pubkeys[0] = val.PubKey
	for i := 1; i < N+1; i++ {
		pubkeys[i] = crypto.GenPrivKeyEd25519().PubKey()
	}

	// build the validator history by running SetBlockAndValidators
	// with the right validator set for each height
	highestHeight := changeHeights[N-1] + 5
	changeIndex := 0
	pubkey := pubkeys[changeIndex]
	for i := int64(1); i < highestHeight; i++ {
		// when we get to a change height,
		// use the next pubkey
		if changeIndex < len(changeHeights) && i == changeHeights[changeIndex] {
			changeIndex++
			pubkey = pubkeys[changeIndex]
		}
		header, parts, responses := makeHeaderPartsResponses(state, i, pubkey)
		state.SetBlockAndValidators(header, parts, responses)
		state.saveValidatorsInfo()
	}

	// make all the test cases by using the same validator until after the change
	testCases := make([]valChangeTestCase, highestHeight)
	changeIndex = 0
	pubkey = pubkeys[changeIndex]
	for i := int64(1); i < highestHeight+1; i++ {
		// we we get to the height after a change height
		// use the next pubkey (note our counter starts at 0 this time)
		if changeIndex < len(changeHeights) && i == changeHeights[changeIndex]+1 {
			changeIndex++
			pubkey = pubkeys[changeIndex]
		}
		testCases[i-1] = valChangeTestCase{i, pubkey}
	}

	for _, testCase := range testCases {
		v, err := state.LoadValidators(testCase.height)
		assert.Nil(err, fmt.Sprintf("expected no err at height %d", testCase.height))
		assert.Equal(v.Size(), 1, "validator set size is greater than 1: %d", v.Size())
		addr, _ := v.GetByIndex(0)

		assert.Equal(addr, testCase.vals.Address(), fmt.Sprintf(`unexpected pubkey at
                height %d`, testCase.height))
	}
}

// TestConsensusParamsChangesSaveLoad tests saving and loading consensus params with changes.
func TestConsensusParamsChangesSaveLoad(t *testing.T) {
	tearDown, _, state := setupTestCase(t)
	defer tearDown(t)
	// nolint: vetshadow
	assert := assert.New(t)

	// change vals at these heights
	changeHeights := []int64{1, 2, 4, 5, 10, 15, 16, 17, 20}
	N := len(changeHeights)

	// each valset is just one validator.
	// create list of them
	params := make([]types.ConsensusParams, N+1)
	params[0] = state.ConsensusParams
	for i := 1; i < N+1; i++ {
		params[i] = *types.DefaultConsensusParams()
		params[i].BlockSize.MaxBytes += i
	}

	// build the params history by running SetBlockAndValidators
	// with the right params set for each height
	highestHeight := changeHeights[N-1] + 5
	changeIndex := 0
	cp := params[changeIndex]
	for i := int64(1); i < highestHeight; i++ {
		// when we get to a change height,
		// use the next params
		if changeIndex < len(changeHeights) && i == changeHeights[changeIndex] {
			changeIndex++
			cp = params[changeIndex]
		}
		header, parts, responses := makeHeaderPartsResponsesParams(state, i, cp)
		state.SetBlockAndValidators(header, parts, responses)
		state.saveConsensusParamsInfo()
	}

	// make all the test cases by using the same params until after the change
	testCases := make([]paramsChangeTestCase, highestHeight)
	changeIndex = 0
	cp = params[changeIndex]
	for i := int64(1); i < highestHeight+1; i++ {
		// we we get to the height after a change height
		// use the next pubkey (note our counter starts at 0 this time)
		if changeIndex < len(changeHeights) && i == changeHeights[changeIndex]+1 {
			changeIndex++
			cp = params[changeIndex]
		}
		testCases[i-1] = paramsChangeTestCase{i, cp}
	}

	for _, testCase := range testCases {
		p, err := state.LoadConsensusParams(testCase.height)
		assert.Nil(err, fmt.Sprintf("expected no err at height %d", testCase.height))
		assert.Equal(testCase.params, p, fmt.Sprintf(`unexpected consensus params at
                height %d`, testCase.height))
	}
}

func TestABCIResults(t *testing.T) {
	a := ABCIResult{Code: 0, Data: nil}
	b := ABCIResult{Code: 0, Data: []byte{}}
	c := ABCIResult{Code: 0, Data: []byte("one")}
	d := ABCIResult{Code: 14, Data: nil}
	e := ABCIResult{Code: 14, Data: []byte("foo")}
	f := ABCIResult{Code: 14, Data: []byte("bar")}

	// nil and []byte{} should produce same hash
	assert.Equal(t, a.Hash(), b.Hash())

	// a and b should be the same, don't go in results
	results := ABCIResults{a, c, d, e, f}

	// make sure each result hashes properly
	var last []byte
	for i, res := range results {
		h := res.Hash()
		assert.NotEqual(t, last, h, "%d", i)
		last = h
	}

	// make sure that we can get a root hash from results
	// and verify proofs
	root := results.Hash()
	assert.NotEmpty(t, root)

	for i, res := range results {
		proof := results.ProveResult(i)
		valid := proof.Verify(i, len(results), res.Hash(), root)
		assert.True(t, valid, "%d", i)
	}
}

func makeParams(blockBytes, blockTx, blockGas, txBytes,
	txGas, partSize int) types.ConsensusParams {

	return types.ConsensusParams{
		BlockSize: types.BlockSize{
			MaxBytes: blockBytes,
			MaxTxs:   blockTx,
			MaxGas:   int64(blockGas),
		},
		TxSize: types.TxSize{
			MaxBytes: txBytes,
			MaxGas:   int64(txGas),
		},
		BlockGossip: types.BlockGossip{
			BlockPartSizeBytes: partSize,
		},
	}
}

func TestApplyUpdates(t *testing.T) {
	initParams := makeParams(1, 2, 3, 4, 5, 6)

	cases := [...]struct {
		init     types.ConsensusParams
		updates  *abci.ConsensusParams
		expected types.ConsensusParams
	}{
		0: {initParams, nil, initParams},
		1: {initParams, &abci.ConsensusParams{}, initParams},
		2: {initParams,
			&abci.ConsensusParams{
				TxSize: &abci.TxSize{
					MaxBytes: 123,
				},
			},
			makeParams(1, 2, 3, 123, 5, 6)},
		3: {initParams,
			&abci.ConsensusParams{
				BlockSize: &abci.BlockSize{
					MaxTxs: 44,
					MaxGas: 55,
				},
			},
			makeParams(1, 44, 55, 4, 5, 6)},
		4: {initParams,
			&abci.ConsensusParams{
				BlockSize: &abci.BlockSize{
					MaxTxs: 789,
				},
				TxSize: &abci.TxSize{
					MaxGas: 888,
				},
				BlockGossip: &abci.BlockGossip{
					BlockPartSizeBytes: 2002,
				},
			},
			makeParams(1, 789, 3, 4, 888, 2002)},
	}

	for i, tc := range cases {
		res := tc.init.Update(tc.updates)
		assert.Equal(t, tc.expected, res, "case %d", i)
	}
}

func makeHeaderPartsResponses(state *State, height int64,
	pubkey crypto.PubKey) (*types.Header, types.PartSetHeader, *ABCIResponses) {

	block := makeBlock(state, height)
	_, val := state.Validators.GetByIndex(0)
	abciResponses := &ABCIResponses{
		Height:   height,
		EndBlock: &abci.ResponseEndBlock{ValidatorUpdates: []*abci.Validator{}},
	}

	// if the pubkey is new, remove the old and add the new
	if !bytes.Equal(pubkey.Bytes(), val.PubKey.Bytes()) {
		abciResponses.EndBlock = &abci.ResponseEndBlock{
			ValidatorUpdates: []*abci.Validator{
				{val.PubKey.Bytes(), 0},
				{pubkey.Bytes(), 10},
			},
		}
	}

	return block.Header, types.PartSetHeader{}, abciResponses
}

type valChangeTestCase struct {
	height int64
	vals   crypto.PubKey
}

func makeHeaderPartsResponsesParams(state *State, height int64,
	params types.ConsensusParams) (*types.Header, types.PartSetHeader, *ABCIResponses) {

	block := makeBlock(state, height)
	abciResponses := &ABCIResponses{
		Height:   height,
		EndBlock: &abci.ResponseEndBlock{ConsensusParamUpdates: types.TM2PB.ConsensusParams(&params)},
	}
	return block.Header, types.PartSetHeader{}, abciResponses
}

type paramsChangeTestCase struct {
	height int64
	params types.ConsensusParams
}
