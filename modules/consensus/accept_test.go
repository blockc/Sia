package consensus

import (
	"bytes"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/NebulousLabs/Sia/crypto"
	"github.com/NebulousLabs/Sia/modules"
	"github.com/NebulousLabs/Sia/types"
)

// TestDoSBlockHandling checks that saved bad blocks are correctly ignored.
func TestDoSBlockHandling(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	cst, err := createConsensusSetTester("TestDoSBlockHandling")
	if err != nil {
		t.Fatal(err)
	}

	// Mine a DoS block and submit it to the state, expect a normal error.
	// Create a transaction that is funded but the funds are never spent. This
	// transaction is invalid in a way that triggers the DoS block detection.
	id, err := cst.wallet.RegisterTransaction(types.Transaction{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = cst.wallet.FundTransaction(id, types.NewCurrency64(50))
	if err != nil {
		t.Fatal(err)
	}
	cst.tpUpdateWait()
	txn, err := cst.wallet.SignTransaction(id, true) // true indicates that the whole transaction should be signed.
	if err != nil {
		t.Fatal(err)
	}

	// Get a block, insert the transaction, and submit the block.
	block, _, target := cst.miner.BlockForWork()
	block.Transactions = append(block.Transactions, txn)
	dosBlock, _ := cst.miner.SolveBlock(block, target)
	err = cst.cs.AcceptBlock(dosBlock)
	if err != ErrSiacoinInputOutputMismatch {
		t.Fatal("expecting invalid signature err: " + err.Error())
	}

	// Submit the same DoS block to the state again, expect ErrDoSBlock.
	err = cst.cs.AcceptBlock(dosBlock)
	if err != ErrDoSBlock {
		t.Fatal("expecting bad block err: " + err.Error())
	}
}

// testBlockKnownHandling submits known blocks to the consensus set.
func (cst *consensusSetTester) testBlockKnownHandling() error {
	// Get a block destined to be stale.
	block, _, target := cst.miner.BlockForWork()
	staleBlock, _ := cst.miner.SolveBlock(block, target)

	// Add two new blocks to the consensus set to block the stale block.
	block1, _ := cst.miner.FindBlock()
	err := cst.cs.AcceptBlock(block1)
	if err != nil {
		return err
	}
	cst.csUpdateWait()
	block2, _ := cst.miner.FindBlock()
	err = cst.cs.AcceptBlock(block2)
	if err != nil {
		return err
	}
	cst.csUpdateWait()

	// Submit the stale block.
	err = cst.cs.acceptBlock(staleBlock)
	if err != nil && err != modules.ErrNonExtendingBlock {
		return err
	}

	// Submit block1 and block2 again, looking for a 'BlockKnown' error.
	err = cst.cs.acceptBlock(block1)
	if err != ErrBlockKnown {
		return errors.New("expecting known block err: " + err.Error())
	}
	err = cst.cs.acceptBlock(block2)
	if err != ErrBlockKnown {
		return errors.New("expecting known block err: " + err.Error())
	}
	err = cst.cs.acceptBlock(staleBlock)
	if err != ErrBlockKnown {
		return errors.New("expecting known block err: " + err.Error())
	}

	// Try the genesis block edge case.
	genesisBlock := cst.cs.blockMap[cst.cs.currentPath[0]].block
	err = cst.cs.acceptBlock(genesisBlock)
	if err != ErrBlockKnown {
		return errors.New("expecting known block err: " + err.Error())
	}
	return nil
}

// TestBlockKnownHandling creates a new consensus set tester and uses it to
// call testBlockKnownHandling.
func TestBlockKnownHandling(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}

	cst, err := createConsensusSetTester("TestBlockKnownHandling")
	if err != nil {
		t.Fatal(err)
	}
	err = cst.testBlockKnownHandling()
	if err != nil {
		t.Error(err)
	}
}

// TestOrphanHandling passes an orphan block to the consensus set.
func TestOrphanHandling(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	cst, err := createConsensusSetTester("TestOrphanHandling")
	if err != nil {
		t.Fatal(err)
	}

	// The empty block is an orphan.
	orphan := types.Block{}
	err = cst.cs.acceptBlock(orphan)
	if err != ErrOrphan {
		t.Error("expecting ErrOrphan:", err)
	}
	err = cst.cs.acceptBlock(orphan)
	if err != ErrOrphan {
		t.Error("expecting ErrOrphan:", err)
	}
}

// TestMissedTarget submits a block that does not meet the required target.
func TestMissedTarget(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	cst, err := createConsensusSetTester("TestMissedTarget")
	if err != nil {
		t.Fatal(err)
	}

	// Mine a block that doesn't meet the target.
	block, _, target := cst.miner.BlockForWork()
	for block.CheckTarget(target) && block.Nonce[0] != 255 {
		block.Nonce[0]++
	}
	if block.CheckTarget(target) {
		t.Fatal("unable to find a failing target (lol)")
	}
	err = cst.cs.acceptBlock(block)
	if err != ErrMissedTarget {
		t.Error("expecting ErrMissedTarget:", err)
	}
}

// testLargeBlock creates a block that is too large to be accepted by the state
// and checks that it actually gets rejected.
func TestLargeBlock(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	cst, err := createConsensusSetTester("TestLargeBlock")
	if err != nil {
		t.Fatal(err)
	}

	// Create a transaction that puts the block over the size limit.
	bigData := make([]byte, types.BlockSizeLimit)
	txn := types.Transaction{
		ArbitraryData: [][]byte{bigData},
	}

	// Fetch a block and add the transaction, then submit the block.
	block, _, target := cst.miner.BlockForWork()
	block.Transactions = append(block.Transactions, txn)
	solvedBlock, _ := cst.miner.SolveBlock(block, target)
	err = cst.cs.acceptBlock(solvedBlock)
	if err != ErrLargeBlock {
		t.Error(err)
	}
}

// TestEarlyBlockTimestampHandling checks that blocks with early timestamps are
// handled appropriately.
func TestEarlyBlockTimestampHandling(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	cst, err := createConsensusSetTester("TestBlockTimestampHandling")
	if err != nil {
		t.Fatal(err)
	}

	// Create a block with a too early timestamp - block should be rejected
	// outright.
	block, _, target := cst.miner.BlockForWork()
	earliestTimestamp := cst.cs.blockMap[block.ParentID].earliestChildTimestamp()
	block.Timestamp = earliestTimestamp - 1
	earlyBlock, _ := cst.miner.SolveBlock(block, target)
	err = cst.cs.acceptBlock(earlyBlock)
	if err != ErrEarlyTimestamp {
		t.Error("expecting ErrEarlyTimestamp:", err.Error())
	}
}

// TestExtremeFutureTimestampHandling checks that blocks with extreme future
// timestamps handled correclty.
func TestExtremeFutureTimestampHandling(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	cst, err := createConsensusSetTester("TestExtremeFutureTimestampHandling")
	if err != nil {
		t.Fatal(err)
	}

	// Submit a block with a timestamp in the extreme future.
	block, _, target := cst.miner.BlockForWork()
	block.Timestamp = types.CurrentTimestamp() + 2 + types.ExtremeFutureThreshold
	solvedBlock, _ := cst.miner.SolveBlock(block, target)
	err = cst.cs.acceptBlock(solvedBlock)
	if err != ErrExtremeFutureTimestamp {
		t.Error("Expecting ErrExtremeFutureTimestamp", err)
	}

	// Check that after waiting until the block is no longer in the future, the
	// block still has not been added to the consensus set (prove that the
	// block was correctly discarded).
	time.Sleep(time.Second * time.Duration(3+types.ExtremeFutureThreshold))
	lockID := cst.cs.mu.RLock()
	defer cst.cs.mu.RUnlock(lockID)
	_, exists := cst.cs.blockMap[solvedBlock.ID()]
	if exists {
		t.Error("extreme future block made it into the consensus set after waiting")
	}
}

// TestMinerPayoutHandling checks that blocks with incorrect payouts are
// rejected.
func TestMinerPayoutHandling(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	cst, err := createConsensusSetTester("TestMinerPayoutHandling")
	if err != nil {
		t.Fatal(err)
	}

	// Create a block with the wrong miner payout structure - testing can be
	// light here because there is heavier testing in the 'types' package,
	// where the logic is defined.
	block, _, target := cst.miner.BlockForWork()
	block.MinerPayouts = append(block.MinerPayouts, types.SiacoinOutput{Value: types.NewCurrency64(1)})
	solvedBlock, _ := cst.miner.SolveBlock(block, target)
	err = cst.cs.acceptBlock(solvedBlock)
	if err != ErrBadMinerPayouts {
		t.Error(err)
	}
}

// testFutureTimestampHandling checks that blocks in the future (but not
// extreme future) are handled correctly.
func (cst *consensusSetTester) testFutureTimestampHandling() error {
	// Submit a block with a timestamp in the future, but not the extreme
	// future.
	block, _, target := cst.miner.BlockForWork()
	block.Timestamp = types.CurrentTimestamp() + 2 + types.FutureThreshold
	solvedBlock, _ := cst.miner.SolveBlock(block, target)
	err := cst.cs.acceptBlock(solvedBlock)
	if err != ErrFutureTimestamp {
		return errors.New("Expecting ErrExtremeFutureTimestamp: " + err.Error())
	}

	// Check that after waiting until the block is no longer too far in the
	// future, the block gets added to the consensus set.
	time.Sleep(time.Second * 3) // 3 seconds, as the block was originally 2 seconds too far into the future.
	lockID := cst.cs.mu.RLock()
	defer cst.cs.mu.RUnlock(lockID)
	_, exists := cst.cs.blockMap[solvedBlock.ID()]
	if !exists {
		return errors.New("future block was not added to the consensus set after waiting the appropriate amount of time.")
	}
	return nil
}

// TestFutureTimestampHandling creates a consensus set tester and uses it to
// call testFutureTimestampHandling.
func TestFutureTimestampHandling(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	cst, err := createConsensusSetTester("TestFutureTimestampHandling")
	if err != nil {
		t.Fatal(err)
	}
	err = cst.testFutureTimestampHandling()
	if err != nil {
		t.Error(err)
	}
}

// TestInconsistentCheck submits a block on a consensus set that is
// inconsistent, attempting to trigger a panic.
func TestInconsistentCheck(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	cst, err := createConsensusSetTester("TestInconsistentCheck")
	if err != nil {
		t.Fatal(err)
	}

	// Corrupt the consensus set.
	var scod types.SiacoinOutputID
	var sco types.SiacoinOutput
	for id, output := range cst.cs.siacoinOutputs {
		scod = id
		sco = output
		break
	}
	sco.Value = sco.Value.Add(types.NewCurrency64(1))
	cst.cs.siacoinOutputs[scod] = sco

	// Mine and submit a block, triggering the inconsistency check.
	defer func() {
		r := recover()
		if r != errSiacoinMiscount {
			t.Error("expecting errSiacoinMiscount, got:", r)
		}
	}()
	block, _ := cst.miner.FindBlock()
	_ = cst.cs.AcceptBlock(block)
}

// testSimpleBlock mines a simple block (no transactions except those
// automatically added by the miner) and adds it to the consnesus set.
func (cst *consensusSetTester) testSimpleBlock() error {
	// Get the starting hash of the consenesus set.
	initialCSSum := cst.cs.consensusSetHash()

	// Mine and submit a block
	block, _ := cst.miner.FindBlock()
	err := cst.cs.AcceptBlock(block)
	if err != nil {
		return err
	}
	cst.csUpdateWait()

	// Get the ending hash of the consensus set.
	resultingCSSum := cst.cs.consensusSetHash()
	if initialCSSum == resultingCSSum {
		return errors.New("state hash is unchanged after mining a block")
	}

	// Check that the current path has updated as expected.
	newNode := cst.cs.currentBlockNode()
	if cst.cs.CurrentBlock().ID() != block.ID() {
		return errors.New("the state's current block is not reporting as the recently mined block.")
	}
	// Check that the current path has updated correctly.
	if block.ID() != cst.cs.currentPath[newNode.height] {
		return errors.New("the state's current path didn't update correctly after accepting a new block")
	}

	// Revert the block that was just added to the consensus set and check for
	// parity with the original state of consensus.
	_, _, err = cst.cs.forkBlockchain(newNode.parent)
	if err != nil {
		return err
	}
	if cst.cs.consensusSetHash() != initialCSSum {
		return errors.New("adding and reverting a block changed the consensus set")
	}
	// Re-add the block and check for parity with the first time it was added.
	// This test is useful because a different codepath is followed if the
	// diffs have already been generated.
	_, _, err = cst.cs.forkBlockchain(newNode)
	if cst.cs.consensusSetHash() != resultingCSSum {
		return errors.New("adding, reverting, and reading a block was inconsistent with just adding the block")
	}
	return nil
}

// TestSimpleBlock creates a consensus set tester and uses it to call
// testSimpleBlock.
func TestSimpleBlock(t *testing.T) {
	cst, err := createConsensusSetTester("TestSimpleBlock")
	if err != nil {
		t.Fatal(err)
	}
	err = cst.testSimpleBlock()
	if err != nil {
		t.Error(err)
	}
}

// testSpendSiacoinsBlock mines a block with a transaction spending siacoins
// and adds it to the consensus set.
func (cst *consensusSetTester) testSpendSiacoinsBlock() error {
	// Create a random destination address for the output in the transaction.
	var destAddr types.UnlockHash
	_, err := rand.Read(destAddr[:])
	if err != nil {
		return err
	}

	// Create a block containing a transaction with a valid siacoin output.
	txnValue := types.NewCurrency64(1200)
	id, err := cst.wallet.RegisterTransaction(types.Transaction{})
	if err != nil {
		return err
	}
	_, err = cst.wallet.FundTransaction(id, txnValue)
	if err != nil {
		return err
	}
	cst.tpUpdateWait()
	_, outputIndex, err := cst.wallet.AddSiacoinOutput(id, types.SiacoinOutput{Value: txnValue, UnlockHash: destAddr})
	if err != nil {
		return err
	}
	txn, err := cst.wallet.SignTransaction(id, true)
	if err != nil {
		return err
	}
	err = cst.tpool.AcceptTransaction(txn)
	if err != nil {
		return err
	}
	cst.tpUpdateWait()
	outputID := txn.SiacoinOutputID(int(outputIndex))

	// Mine and apply the block to the consensus set.
	block, _ := cst.miner.FindBlock()
	err = cst.cs.AcceptBlock(block)
	if err != nil {
		return err
	}
	cst.csUpdateWait()

	// Find the destAddr among the outputs.
	var found bool
	for id, output := range cst.cs.siacoinOutputs {
		if id == outputID {
			if found {
				return errors.New("output found twice")
			}
			if output.Value.Cmp(txnValue) != 0 {
				return errors.New("output has wrong value")
			}
			found = true
		}
	}
	if !found {
		return errors.New("could not find created siacoin output")
	}
	return nil
}

// TestSpendSiacoinsBlock creates a consensus set tester and uses it to call
// testSpendSiacoinsBlock.
func TestSpendSiacoinsBlock(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	cst, err := createConsensusSetTester("TestSpendSiacoinsBlock")
	if err != nil {
		t.Fatal(err)
	}
	err = cst.testSpendSiacoinsBlock()
	if err != nil {
		t.Error(err)
	}
}

// testFileContractsBlocks creates a series of blocks that create, revise,
// prove, and fail to prove file contracts.
func (cst *consensusSetTester) testFileContractsBlocks() error {
	var validProofDest, missedProofDest, revisionDest types.UnlockHash
	_, err := rand.Read(validProofDest[:])
	if err != nil {
		return err
	}
	_, err = rand.Read(missedProofDest[:])
	if err != nil {
		return err
	}
	_, err = rand.Read(revisionDest[:])
	if err != nil {
		return err
	}

	// Create a file (as a bytes.Buffer) that will be used for file contracts
	// and storage proofs.
	filesize := uint64(4e3)
	fileBytes := make([]byte, filesize)
	_, err = rand.Read(fileBytes)
	if err != nil {
		return err
	}
	file := bytes.NewReader(fileBytes)
	merkleRoot, err := crypto.ReaderMerkleRoot(file)
	if err != nil {
		return err
	}
	file.Seek(0, 0)

	// Create a file contract that will be successfully proven and an alternate
	// file contract which will be missed.
	payout := types.NewCurrency64(400e6)
	validFC := types.FileContract{
		FileSize:       filesize,
		FileMerkleRoot: merkleRoot,
		WindowStart:    cst.cs.height() + 2,
		WindowEnd:      cst.cs.height() + 4,
		Payout:         payout,
		ValidProofOutputs: []types.SiacoinOutput{{
			UnlockHash: validProofDest,
		}},
		MissedProofOutputs: []types.SiacoinOutput{{
			UnlockHash: missedProofDest,
		}},
		UnlockHash: types.UnlockConditions{}.UnlockHash(),
	}
	outputSize := payout.Sub(validFC.Tax())
	validFC.ValidProofOutputs[0].Value = outputSize
	validFC.MissedProofOutputs[0].Value = outputSize
	missedFC := types.FileContract{
		FileSize:       uint64(filesize),
		FileMerkleRoot: merkleRoot,
		WindowStart:    cst.cs.height() + 2,
		WindowEnd:      cst.cs.height() + 4,
		Payout:         payout,
		ValidProofOutputs: []types.SiacoinOutput{{
			Value:      outputSize,
			UnlockHash: validProofDest,
		}},
		MissedProofOutputs: []types.SiacoinOutput{{
			Value:      outputSize,
			UnlockHash: missedProofDest,
		}},
		UnlockHash: types.UnlockConditions{}.UnlockHash(),
	}

	// Create and fund a transaction with a file contract.
	id, err := cst.wallet.RegisterTransaction(types.Transaction{})
	if err != nil {
		return err
	}
	_, err = cst.wallet.FundTransaction(id, payout.Mul(types.NewCurrency64(2)))
	if err != nil {
		return err
	}
	cst.tpUpdateWait()
	_, validFCIndex, err := cst.wallet.AddFileContract(id, validFC)
	if err != nil {
		return err
	}
	_, missedFCIndex, err := cst.wallet.AddFileContract(id, missedFC)
	if err != nil {
		return err
	}
	txn, err := cst.wallet.SignTransaction(id, true)
	if err != nil {
		return err
	}
	validFCID := txn.FileContractID(int(validFCIndex))
	missedFCID := txn.FileContractID(int(missedFCIndex))
	err = cst.tpool.AcceptTransaction(txn)
	if err != nil {
		return err
	}
	cst.tpUpdateWait()
	block, _ := cst.miner.FindBlock()
	err = cst.cs.AcceptBlock(block)
	if err != nil {
		return err
	}
	cst.csUpdateWait()

	// Check that the siafund pool was increased.
	if cst.cs.siafundPool.Cmp(types.NewCurrency64(31200e3)) != 0 {
		return errors.New("siafund pool was not increased correctly")
	}

	// Submit a file contract revision to the missed-proof file contract.
	txn = types.Transaction{
		FileContractRevisions: []types.FileContractRevision{{
			ParentID:          missedFCID,
			NewRevisionNumber: 1,

			NewFileSize:          10e3, // By changing the filesize without changing the hash, a proof should become impossible.
			NewFileMerkleRoot:    missedFC.FileMerkleRoot,
			NewWindowStart:       missedFC.WindowStart + 1,
			NewWindowEnd:         missedFC.WindowEnd,
			NewValidProofOutputs: missedFC.ValidProofOutputs,
			NewMissedProofOutputs: []types.SiacoinOutput{{
				Value:      outputSize,
				UnlockHash: revisionDest,
			}},
		}},
	}
	err = cst.tpool.AcceptTransaction(txn)
	if err != nil {
		return err
	}
	cst.tpUpdateWait()
	block, _ = cst.miner.FindBlock()
	err = cst.cs.AcceptBlock(block)
	if err != nil {
		return err
	}
	cst.csUpdateWait()

	// Check that the revision was successful.
	if cst.cs.fileContracts[missedFCID].RevisionNumber != 1 {
		return errors.New("revision did not update revision number")
	}
	if cst.cs.fileContracts[missedFCID].FileSize != 10e3 {
		return errors.New("revision did not update file contract size")
	}

	// Create a storage proof for the validFC and submit it in a block.
	spSegmentIndex, err := cst.cs.StorageProofSegment(validFCID)
	if err != nil {
		return err
	}
	segment, hashSet, err := crypto.BuildReaderProof(file, spSegmentIndex)
	if err != nil {
		return err
	}
	txn = types.Transaction{
		StorageProofs: []types.StorageProof{{
			ParentID: validFCID,
			Segment:  segment,
			HashSet:  hashSet,
		}},
	}
	err = cst.tpool.AcceptTransaction(txn)
	if err != nil {
		return err
	}
	cst.tpUpdateWait()
	block, _ = cst.miner.FindBlock()
	err = cst.cs.AcceptBlock(block)
	if err != nil {
		return err
	}
	cst.csUpdateWait()

	// Check that the valid contract was removed but the missed contract was
	// not.
	_, exists := cst.cs.fileContracts[validFCID]
	if exists {
		return errors.New("valid file contract still exists in the consensus set")
	}
	_, exists = cst.cs.fileContracts[missedFCID]
	if !exists {
		return errors.New("missed file contract was consumed by storage proof")
	}

	// Check that the file contract output made it into the set of delayed
	// outputs.
	validProofID := validFCID.StorageProofOutputID(types.ProofValid, 0)
	_, exists = cst.cs.delayedSiacoinOutputs[cst.cs.height()+types.MaturityDelay][validProofID]
	if !exists {
		return errors.New("file contract payout is not in the delayed outputs set")
	}

	// Mine a block to close the window on the missed file contract.
	block, _ = cst.miner.FindBlock()
	err = cst.cs.AcceptBlock(block)
	if err != nil {
		return err
	}
	cst.csUpdateWait()
	_, exists = cst.cs.fileContracts[validFCID]
	if exists {
		return errors.New("valid file contract still exists in the consensus set")
	}
	_, exists = cst.cs.fileContracts[missedFCID]
	if exists {
		return errors.New("missed file contract was not consumed when the window was closed.")
	}

	// Mine enough blocks to get all of the outputs into the set of siacoin
	// outputs.
	for i := types.BlockHeight(0); i <= types.MaturityDelay; i++ {
		block, _ = cst.miner.FindBlock()
		err = cst.cs.AcceptBlock(block)
		if err != nil {
			return err
		}
		cst.csUpdateWait()
	}

	// Check that all of the outputs have ended up at the right destination.
	if cst.cs.siacoinOutputs[validFCID.StorageProofOutputID(types.ProofValid, 0)].UnlockHash != validProofDest {
		return errors.New("file contract output did not end up at the right place.")
	}
	if cst.cs.siacoinOutputs[missedFCID.StorageProofOutputID(types.ProofMissed, 0)].UnlockHash != revisionDest {
		return errors.New("missed file proof output did not end up at the revised destination")
	}

	return nil
}

// TestFileContractsBlocks creates a consensus set tester and uses it to call
// testFileContractsBlocks.
func TestFileContractsBlocks(t *testing.T) {
	if testing.Short() {
		// t.SkipNow()
	}
	cst, err := createConsensusSetTester("TestFileContractsBlocks")
	if err != nil {
		t.Fatal(err)
	}
	err = cst.testFileContractsBlocks()
	if err != nil {
		t.Fatal(err)
	}
}

// testSpendSiafundsBlock mines a block with a transaction spending siafunds
// and adds it to the consensus set.
func (cst *consensusSetTester) testSpendSiafundsBlock() error {
	// Create a destination for the siafunds.
	var destAddr types.UnlockHash
	_, err := rand.Read(destAddr[:])
	if err != nil {
		return err
	}

	// Find the siafund output that is 'anyone can spend' (output exists only
	// in the testing setup).
	var srcID types.SiafundOutputID
	var srcValue types.Currency
	anyoneSpends := types.UnlockConditions{}.UnlockHash()
	for id, sfo := range cst.cs.siafundOutputs {
		if sfo.UnlockHash == anyoneSpends {
			srcID = id
			srcValue = sfo.Value
			break
		}
	}

	// Create a transaction that spends siafunds.
	txn := types.Transaction{
		SiafundInputs: []types.SiafundInput{{
			ParentID:         srcID,
			UnlockConditions: types.UnlockConditions{},
		}},
		SiafundOutputs: []types.SiafundOutput{
			{
				Value:      srcValue.Sub(types.NewCurrency64(1)),
				UnlockHash: types.UnlockConditions{}.UnlockHash(),
			},
			{
				Value:      types.NewCurrency64(1),
				UnlockHash: destAddr,
			},
		},
	}
	sfoid0 := txn.SiafundOutputID(0)
	sfoid1 := txn.SiafundOutputID(1)
	cst.tpool.AcceptTransaction(txn)
	cst.tpUpdateWait()

	// Mine a block containing the txn.
	block, _ := cst.miner.FindBlock()
	err = cst.cs.AcceptBlock(block)
	if err != nil {
		return err
	}
	cst.csUpdateWait()

	// Check that the input got consumed, and that the outputs got created.
	_, exists := cst.cs.siafundOutputs[srcID]
	if exists {
		return errors.New("siafund output was not properly consumed")
	}
	sfo, exists := cst.cs.siafundOutputs[sfoid0]
	if !exists {
		return errors.New("siafund output was not properly created")
	}
	if sfo.Value.Cmp(srcValue.Sub(types.NewCurrency64(1))) != 0 {
		return errors.New("created siafund has wrong value")
	}
	if sfo.UnlockHash != anyoneSpends {
		return errors.New("siafund output sent to wrong unlock hash")
	}
	sfo, exists = cst.cs.siafundOutputs[sfoid1]
	if !exists {
		return errors.New("second siafund output was not properly created")
	}
	if sfo.Value.Cmp(types.NewCurrency64(1)) != 0 {
		return errors.New("second siafund output has wrong value")
	}
	if sfo.UnlockHash != destAddr {
		return errors.New("second siafund output sent to wrong addr")
	}

	// Put a file contract into the blockchain that will add values to siafund
	// outputs.
	oldSiafundPool := cst.cs.siafundPool
	payout := types.NewCurrency64(400e6)
	fc := types.FileContract{
		WindowStart: cst.cs.height() + 2,
		WindowEnd:   cst.cs.height() + 4,
		Payout:      payout,
	}
	outputSize := payout.Sub(fc.Tax())
	fc.ValidProofOutputs = []types.SiacoinOutput{{Value: outputSize}}
	fc.MissedProofOutputs = []types.SiacoinOutput{{Value: outputSize}}

	// Create and fund a transaction with a file contract.
	id, err := cst.wallet.RegisterTransaction(types.Transaction{})
	if err != nil {
		return err
	}
	_, err = cst.wallet.FundTransaction(id, payout)
	if err != nil {
		return err
	}
	cst.tpUpdateWait()
	_, _, err = cst.wallet.AddFileContract(id, fc)
	if err != nil {
		return err
	}
	txn, err = cst.wallet.SignTransaction(id, true)
	if err != nil {
		return err
	}
	err = cst.tpool.AcceptTransaction(txn)
	if err != nil {
		return err
	}
	cst.tpUpdateWait()
	block, _ = cst.miner.FindBlock()
	err = cst.cs.AcceptBlock(block)
	if err != nil {
		return err
	}
	cst.csUpdateWait()
	if cst.cs.siafundPool.Cmp(types.NewCurrency64(15600e3).Add(oldSiafundPool)) != 0 {
		return errors.New("siafund pool did not update correctly")
	}

	// Create a transaction that spends siafunds.
	var claimDest types.UnlockHash
	_, err = rand.Read(claimDest[:])
	if err != nil {
		return err
	}
	var srcClaimStart types.Currency
	for id, sfo := range cst.cs.siafundOutputs {
		if sfo.UnlockHash == anyoneSpends {
			srcID = id
			srcValue = sfo.Value
			srcClaimStart = sfo.ClaimStart
			break
		}
	}
	txn = types.Transaction{
		SiafundInputs: []types.SiafundInput{{
			ParentID:         srcID,
			UnlockConditions: types.UnlockConditions{},
			ClaimUnlockHash:  claimDest,
		}},
		SiafundOutputs: []types.SiafundOutput{
			{
				Value:      srcValue.Sub(types.NewCurrency64(1)),
				UnlockHash: types.UnlockConditions{}.UnlockHash(),
			},
			{
				Value:      types.NewCurrency64(1),
				UnlockHash: destAddr,
			},
		},
	}
	sfoid1 = txn.SiafundOutputID(1)
	cst.tpool.AcceptTransaction(txn)
	cst.tpUpdateWait()
	block, _ = cst.miner.FindBlock()
	err = cst.cs.AcceptBlock(block)
	if err != nil {
		return err
	}
	cst.csUpdateWait()

	// Find the siafund output and check that it has the expected number of
	// siafunds.
	found := false
	expectedBalance := cst.cs.siafundPool.Sub(srcClaimStart).Div(types.NewCurrency64(10e3)).Mul(srcValue)
	for _, output := range cst.cs.delayedSiacoinOutputs[cst.cs.height()+types.MaturityDelay] {
		if output.UnlockHash == claimDest {
			found = true
			if output.Value.Cmp(expectedBalance) != 0 {
				return errors.New("siafund output has the wrong balance")
			}
		}
	}
	if !found {
		return errors.New("could not find siafund claim output")
	}

	return nil
}

// TestSpendSiafundsBlock creates a consensus set tester and uses it to call
// testSpendSiafundsBlock.
func TestSpendSiafundsBlock(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	cst, err := createConsensusSetTester("TestSpendSiafundsBlock")
	if err != nil {
		t.Fatal(err)
	}
	err = cst.testSpendSiafundsBlock()
	if err != nil {
		t.Error(err)
	}
}

// TODO:
//
// testPaymentChannel walks through creating a payment channel on the
// blockchain.

// complexBlockSet puts a set of blocks with many types of transactions into
// the consensus set.
func (cst *consensusSetTester) complexBlockSet() error {
	err := cst.testSimpleBlock()
	if err != nil {
		return err
	}
	err = cst.testSpendSiacoinsBlock()
	if err != nil {
		return err
	}
	err = cst.testFileContractsBlocks()
	if err != nil {
		return err
	}
	err = cst.testSpendSiafundsBlock()
	if err != nil {
		return err
	}
	return nil
}

// TestComplexForking adds every type of test block into two parallel chains of
// consensus, and then forks to a new chain, forcing the whole structure to be
// reverted.
func TestComplexForking(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	cst1, err := createConsensusSetTester("TestComplexForking - 1")
	if err != nil {
		t.Fatal(err)
	}
	cst2, err := createConsensusSetTester("TestComplexForking - 2")
	if err != nil {
		t.Fatal(err)
	}
	cst3, err := createConsensusSetTester("TestComplexForking - 3")
	if err != nil {
		t.Fatal(err)
	}

	// Give each type of major block to cst1.
	err = cst1.complexBlockSet()
	if err != nil {
		t.Fatal(err)
	}

	// Give all the blocks in cst1 to cst3 - as a holding place.
	var cst1Blocks []types.Block
	bn := cst1.cs.currentBlockNode()
	for bn != cst1.cs.blockRoot {
		cst1Blocks = append([]types.Block{bn.block}, cst1Blocks...) // prepend
		bn = bn.parent
	}
	for _, block := range cst1Blocks {
		// Some blocks will return errors.
		err = cst3.cs.AcceptBlock(block)
		if err == nil {
			cst3.csUpdateWait()
		}
	}
	if cst3.cs.currentBlockID() != cst1.cs.currentBlockID() {
		t.Error("cst1 and cst3 do not share the same path")
	}
	if cst3.cs.consensusSetHash() != cst1.cs.consensusSetHash() {
		t.Error("cst1 and cst3 do not share a consensus set hash")
	}

	// Mine 3 blocks on cst2, then all the block types, to give it a heavier
	// weight, then give all of its blocks to cst1. This will cause a complex
	// fork to happen.
	for i := 0; i < 3; i++ {
		block, _ := cst2.miner.FindBlock()
		err = cst2.cs.AcceptBlock(block)
		if err != nil {
			t.Fatal(err)
		}
		cst2.csUpdateWait()
	}
	err = cst2.complexBlockSet()
	if err != nil {
		t.Fatal(err)
	}
	var cst2Blocks []types.Block
	bn = cst2.cs.currentBlockNode()
	for bn != cst2.cs.blockRoot {
		cst2Blocks = append([]types.Block{bn.block}, cst2Blocks...) // prepend
		bn = bn.parent
	}
	for _, block := range cst2Blocks {
		// Some blocks will return errors.
		err = cst1.cs.AcceptBlock(block)
		if err == nil {
			cst1.csUpdateWait()
		}
	}
	if cst1.cs.currentBlockID() != cst2.cs.currentBlockID() {
		t.Error("cst1 and cst2 do not share the same path")
	}
	if cst1.cs.consensusSetHash() != cst2.cs.consensusSetHash() {
		t.Error("cst1 and cst2 do not share the same consensus set hash")
	}

	// Mine 6 blocks on cst3 and then give those blocks to cst1, which will
	// cause cst1 to switch back to its old chain. cst1 will then have created,
	// reverted, and reapplied all the significant types of blocks.
	for i := 0; i < 6; i++ {
		block, _ := cst3.miner.FindBlock()
		err = cst3.cs.AcceptBlock(block)
		if err != nil {
			t.Fatal(err)
		}
		cst3.csUpdateWait()
	}
	var cst3Blocks []types.Block
	bn = cst3.cs.currentBlockNode()
	for bn != cst3.cs.blockRoot {
		cst3Blocks = append([]types.Block{bn.block}, cst3Blocks...) // prepend
		bn = bn.parent
	}
	for _, block := range cst3Blocks {
		// Some blocks will return errors.
		err = cst1.cs.AcceptBlock(block)
		if err == nil {
			cst1.csUpdateWait()
		}
	}
	if cst1.cs.currentBlockID() != cst3.cs.currentBlockID() {
		t.Error("cst1 and cst3 do not share the same path")
	}
	if cst1.cs.consensusSetHash() != cst3.cs.consensusSetHash() {
		t.Error("cst1 and cst3 do not share the same consensus set hash")
	}
}

// TestBuriedBadFork creates a block with an invalid transaction that's not on
// the longest fork. The consensus set will not validate that block. Then valid
// blocks are added on top of it to make it the longest fork. When it becomes
// the longest fork, all the blocks should be fully validated and thrown out
// because a parent is invalid.
func TestBuriedBadFork(t *testing.T) {
	if !testing.Short() {
		t.SkipNow()
	}
	cst, err := createConsensusSetTester("TestBuriedBadFork")
	if err != nil {
		t.Fatal(err)
	}
	bn := cst.cs.currentBlockNode()

	// Create a bad block that builds on a parent, so that it is part of not
	// the longest fork.
	badBlock := types.Block{
		ParentID:     bn.parent.block.ID(),
		Timestamp:    types.CurrentTimestamp(),
		MinerPayouts: []types.SiacoinOutput{{Value: types.CalculateCoinbase(bn.height)}},
		Transactions: []types.Transaction{{
			SiacoinInputs: []types.SiacoinInput{{}}, // Will trigger an error on full verification but not partial verification.
		}},
	}
	badBlock, _ = cst.miner.SolveBlock(badBlock, bn.parent.childTarget)
	err = cst.cs.AcceptBlock(badBlock)
	if err != modules.ErrNonExtendingBlock {
		t.Fatal(err)
	}

	// Build another bock on top of the bad block that is fully valid, this
	// will cause a fork and full validation of the bad block, both the bad
	// block and this block should be thrown away.
	block := types.Block{
		ParentID:     badBlock.ID(),
		Timestamp:    types.CurrentTimestamp(),
		MinerPayouts: []types.SiacoinOutput{{Value: types.CalculateCoinbase(bn.height + 1)}},
	}
	block, _ = cst.miner.SolveBlock(block, bn.parent.childTarget) // okay because the target will not change
	err = cst.cs.AcceptBlock(block)
	if err == nil {
		t.Fatal(err)
	}
	_, exists := cst.cs.blockMap[badBlock.ID()]
	if exists {
		t.Error("bad block not cleared from memory")
	}
	_, exists = cst.cs.blockMap[block.ID()]
	if exists {
		t.Error("block not cleared from memory")
	}
}

// TODO:
//
// try to make a block with a buried bad transaction.
