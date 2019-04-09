// Copyright (c) 2018 Uber Technologies, Inc.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

package sql

import (
	"fmt"
	"time"

	"database/sql"
	"encoding/json"

	"github.com/go-sql-driver/mysql"
	"github.com/uber-common/bark"
	"github.com/uber/cadence/.gen/go/shared"
	"github.com/uber/cadence/common"
	p "github.com/uber/cadence/common/persistence"
	"github.com/uber/cadence/common/persistence/sql/storage"
	"github.com/uber/cadence/common/persistence/sql/storage/sqldb"
	"github.com/uber/cadence/common/service/config"
)

type sqlHistoryV2Manager struct {
	sqlStore
	shardID int
}

// newHistoryPersistence creates an instance of HistoryManager
func newHistoryV2Persistence(cfg config.SQL, logger bark.Logger) (p.HistoryV2Store, error) {
	var db, err = storage.NewSQLDB(&cfg)
	if err != nil {
		return nil, err
	}
	return &sqlHistoryV2Manager{
		sqlStore: sqlStore{
			db:     db,
			logger: logger,
		},
	}, nil
}

func (m *sqlHistoryV2Manager) serializeAncestors(ans []*shared.HistoryBranchRange) ([]byte, error) {
	ancestors, err := json.Marshal(ans)
	if err != nil {
		return nil, err
	}
	return ancestors, nil
}

func (m *sqlHistoryV2Manager) deserializeAncestors(jsonStr []byte) ([]*shared.HistoryBranchRange, error) {
	var ans []*shared.HistoryBranchRange
	err := json.Unmarshal(jsonStr, &ans)
	if err != nil {
		return nil, err
	}
	return ans, nil
}

// AppendHistoryNodes add(or override) a node to a history branch
func (m *sqlHistoryV2Manager) AppendHistoryNodes(request *p.InternalAppendHistoryNodesRequest) error {
	branchInfo := request.BranchInfo
	beginNodeID := p.GetBeginNodeID(branchInfo)

	if request.NodeID < beginNodeID {
		return &p.InvalidPersistenceRequestError{
			Msg: fmt.Sprintf("cannot append to ancestors' nodes"),
		}
	}

	nodeRow := &sqldb.HistoryNodeRow{
		TreeID:       sqldb.MustParseUUID(branchInfo.GetTreeID()),
		BranchID:     sqldb.MustParseUUID(branchInfo.GetBranchID()),
		NodeID:       request.NodeID,
		TxnID:        &request.TransactionID,
		Data:         request.Events.Data,
		DataEncoding: string(request.Events.Encoding),
	}

	if request.IsNewBranch {
		var ans []*shared.HistoryBranchRange
		for _, anc := range branchInfo.Ancestors {
			ans = append(ans, anc)
		}

		ancestors, err := m.serializeAncestors(ans)
		if err != nil {
			return err
		}
		treeRow := &sqldb.HistoryTreeRow{
			TreeID:     sqldb.MustParseUUID(branchInfo.GetTreeID()),
			BranchID:   sqldb.MustParseUUID(branchInfo.GetBranchID()),
			InProgress: false,
			CreatedTs:  time.Now(),
			Ancestors:  ancestors,
			Info:       request.Info,
		}

		return m.txExecute("AppendHistoryNodes", func(tx sqldb.Tx) error {
			result, err := tx.InsertIntoHistoryNode(nodeRow)
			if err != nil {
				return err
			}
			rowsAffected, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if rowsAffected != 1 {
				return fmt.Errorf("expected 1 row to be affected for node table, got %v", rowsAffected)
			}
			result, err = tx.InsertIntoHistoryTree(treeRow)
			if err != nil {
				return err
			}
			rowsAffected, err = result.RowsAffected()
			if err != nil {
				return err
			}
			if rowsAffected != 1 {
				return fmt.Errorf("expected 1 row to be affected for tree table, got %v", rowsAffected)
			}
			return nil
		})
	}

	_, err := m.db.InsertIntoHistoryNode(nodeRow)
	if err != nil {
		if sqlErr, ok := err.(*mysql.MySQLError); ok && sqlErr.Number == ErrDupEntry {
			return &p.ConditionFailedError{Msg: fmt.Sprintf("AppendHistoryNodes: row already exist: %v", err)}
		}
		return &shared.InternalServiceError{Message: fmt.Sprintf("AppendHistoryEvents: %v", err)}
	}
	return nil
}

// ReadHistoryBranch returns history node data for a branch
func (m *sqlHistoryV2Manager) ReadHistoryBranch(request *p.InternalReadHistoryBranchRequest) (*p.InternalReadHistoryBranchResponse, error) {
	minNodeID := request.MinNodeID

	if request.NextPageToken != nil && len(request.NextPageToken) > 0 {
		var lastNodeID int64
		var err error
		if lastNodeID, err = deserializePageToken(request.NextPageToken); err != nil {
			return nil, &shared.InternalServiceError{
				Message: fmt.Sprintf("invalid next page token %v", request.NextPageToken)}
		}
		minNodeID = lastNodeID + 1
	}

	filter := &sqldb.HistoryNodeFilter{
		TreeID:    sqldb.MustParseUUID(request.TreeID),
		BranchID:  sqldb.MustParseUUID(request.BranchID),
		MinNodeID: &minNodeID,
		MaxNodeID: &request.MaxNodeID,
		PageSize:  &request.PageSize,
	}

	rows, err := m.db.SelectFromHistoryNode(filter)
	if err == sql.ErrNoRows || (err == nil && len(rows) == 0) {
		return &p.InternalReadHistoryBranchResponse{}, nil
	}

	history := make([]*p.DataBlob, 0, int(request.PageSize))
	lastNodeID := int64(-1)
	lastTxnID := int64(-1)
	eventBlob := &p.DataBlob{}

	for _, row := range rows {
		eventBlob.Data = row.Data
		eventBlob.Encoding = common.EncodingType(row.DataEncoding)
		switch {
		case row.NodeID < lastNodeID:
			return nil, &shared.InternalServiceError{
				Message: fmt.Sprintf("corrupted data, nodeID cannot decrease"),
			}
		case row.NodeID == lastNodeID:
			if *row.TxnID < lastTxnID {
				// skip the nodes with smaller txn_id
				continue
			} else {
				return nil, &shared.InternalServiceError{
					Message: fmt.Sprintf("corrupted data, same nodeID must have smaller txnID"),
				}
			}
		default: // row.NodeID > lastNodeID:
			// NOTE: when row.nodeID > lastNodeID, we expect the one with largest txnID comes first
			lastTxnID = *row.TxnID
			lastNodeID = row.NodeID
			history = append(history, eventBlob)
			eventBlob = &p.DataBlob{}
		}
	}

	var pagingToken []byte
	if len(rows) >= request.PageSize {
		pagingToken = serializePageToken(lastNodeID)
	}
	response := &p.InternalReadHistoryBranchResponse{
		History:       history,
		NextPageToken: pagingToken,
	}

	return response, nil
}

// ForkHistoryBranch forks a new branch from an existing branch
// Note that application must provide a void forking nodeID, it must be a valid nodeID in that branch.
// A valid forking nodeID can be an ancestor from the existing branch.
// For example, we have branch B1 with three nodes(1[1,2], 3[3,4,5] and 6[6,7,8]. 1, 3 and 6 are nodeIDs (first eventID of the batch).
// So B1 looks like this:
//           1[1,2]
//           /
//         3[3,4,5]
//        /
//      6[6,7,8]
//
// Assuming we have branch B2 which contains one ancestor B1 stopping at 6 (exclusive). So B2 inherit nodeID 1 and 3 from B1, and have its own nodeID 6 and 8.
// Branch B2 looks like this:
//           1[1,2]
//           /
//         3[3,4,5]
//          \
//           6[6,7]
//           \
//            8[8]
//
//Now we want to fork a new branch B3 from B2.
// The only valid forking nodeIDs are 3,6 or 8.
// 1 is not valid because we can't fork from first node.
// 2/4/5 is NOT valid either because they are inside a batch.
//
// Case #1: If we fork from nodeID 6, then B3 will have an ancestor B1 which stops at 6(exclusive).
// As we append a batch of events[6,7,8,9] to B3, it will look like :
//           1[1,2]
//           /
//         3[3,4,5]
//          \
//         6[6,7,8,9]
//
// Case #2: If we fork from node 8, then B3 will have two ancestors: B1 stops at 6(exclusive) and ancestor B2 stops at 8(exclusive)
// As we append a batch of events[8,9] to B3, it will look like:
//           1[1,2]
//           /
//         3[3,4,5]
//        /
//      6[6,7]
//       \
//       8[8,9]
//
func (m *sqlHistoryV2Manager) ForkHistoryBranch(request *p.InternalForkHistoryBranchRequest) (*p.InternalForkHistoryBranchResponse, error) {
	forkB := request.ForkBranchInfo
	treeID := *forkB.TreeID
	newAncestors := make([]*shared.HistoryBranchRange, 0, len(forkB.Ancestors)+1)

	beginNodeID := p.GetBeginNodeID(forkB)
	if beginNodeID >= request.ForkNodeID {
		// this is the case that new branch's ancestors doesn't include the forking branch
		for _, br := range forkB.Ancestors {
			if *br.EndNodeID >= request.ForkNodeID {
				newAncestors = append(newAncestors, &shared.HistoryBranchRange{
					BranchID:    br.BranchID,
					BeginNodeID: br.BeginNodeID,
					EndNodeID:   common.Int64Ptr(request.ForkNodeID),
				})
				break
			} else {
				newAncestors = append(newAncestors, br)
			}
		}
	} else {
		// this is the case the new branch will inherit all ancestors from forking branch
		newAncestors = forkB.Ancestors
		newAncestors = append(newAncestors, &shared.HistoryBranchRange{
			BranchID:    forkB.BranchID,
			BeginNodeID: common.Int64Ptr(beginNodeID),
			EndNodeID:   common.Int64Ptr(request.ForkNodeID),
		})
	}

	resp := &p.InternalForkHistoryBranchResponse{
		NewBranchInfo: shared.HistoryBranch{
			TreeID:    &treeID,
			BranchID:  &request.NewBranchID,
			Ancestors: newAncestors,
		}}

	ancestors, err := m.serializeAncestors(newAncestors)
	if err != nil {
		return nil, err
	}

	row := &sqldb.HistoryTreeRow{
		TreeID:     sqldb.MustParseUUID(treeID),
		BranchID:   sqldb.MustParseUUID(request.NewBranchID),
		InProgress: true,
		CreatedTs:  time.Now(),
		Ancestors:  ancestors,
		Info:       request.Info,
	}
	result, err := m.db.InsertIntoHistoryTree(row)
	if err != nil {
		return nil, err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if rowsAffected != 1 {
		return nil, fmt.Errorf("expected 1 row to be affected for tree table, got %v", rowsAffected)
	}
	return resp, nil
}

// DeleteHistoryBranch removes a branch
func (m *sqlHistoryV2Manager) DeleteHistoryBranch(request *p.InternalDeleteHistoryBranchRequest) error {
	branch := request.BranchInfo
	treeID := *branch.TreeID
	brsToDelete := branch.Ancestors
	beginNodeID := p.GetBeginNodeID(branch)
	brsToDelete = append(brsToDelete, &shared.HistoryBranchRange{
		BranchID:    branch.BranchID,
		BeginNodeID: common.Int64Ptr(beginNodeID),
	})

	rsp, err := m.GetHistoryTree(&p.GetHistoryTreeRequest{
		TreeID: treeID,
	})
	if err != nil {
		return err
	}
	// We won't delete the branch if there is any branch forking in progress. We will return error.
	if len(rsp.ForkingInProgressBranches) > 0 {
		return &p.ConditionFailedError{
			Msg: fmt.Sprintf("There are branches in progress of forking"),
		}
	}

	// If there is no branch forking in progress we see here, it means that we are safe to calculate the deleting ranges based on the current result,
	// Because before getting here, we've already deleted mutableState record, so all the forking branches in the future should fail.

	// validBRsMaxEndNode is to for each branch range that is being used, we want to know what is the max nodeID referred by other valid branch
	validBRsMaxEndNode := map[string]int64{}
	for _, b := range rsp.Branches {
		for _, br := range b.Ancestors {
			curr, ok := validBRsMaxEndNode[*br.BranchID]
			if !ok || curr < *br.EndNodeID {
				validBRsMaxEndNode[*br.BranchID] = *br.EndNodeID
			}
		}
	}

	return m.txExecute("DeleteHistoryBranch", func(tx sqldb.Tx) error {
		branchID := sqldb.MustParseUUID(*branch.BranchID)
		treeFilter := &sqldb.HistoryTreeFilter{
			TreeID:   sqldb.MustParseUUID(treeID),
			BranchID: &branchID,
		}
		_, err := tx.DeleteFromHistoryTree(treeFilter)
		if err != nil {
			return err
		}

		done := false
		// for each branch range to delete, we iterate from bottom to up, and delete up to the point according to validBRsEndNode
		for i := len(brsToDelete) - 1; i >= 0; i-- {
			br := brsToDelete[i]
			maxReferredEndNodeID, ok := validBRsMaxEndNode[*br.BranchID]
			nodeFilter := &sqldb.HistoryNodeFilter{
				TreeID:   sqldb.MustParseUUID(treeID),
				BranchID: sqldb.MustParseUUID(*br.BranchID),
			}

			if ok {
				// we can only delete from the maxEndNode and stop here
				nodeFilter.MinNodeID = &maxReferredEndNodeID
				done = true
			} else {
				// No any branch is using this range, we can delete all of it
				nodeFilter.MinNodeID = br.BeginNodeID
			}
			_, err := tx.DeleteFromHistoryNode(nodeFilter)
			if err != nil {
				return err
			}
			if done {
				break
			}
		}
		return nil
	})
}

// UpdateHistoryBranch update a branch
func (m *sqlHistoryV2Manager) CompleteForkBranch(request *p.InternalCompleteForkBranchRequest) error {
	branch := request.BranchInfo
	treeID := sqldb.MustParseUUID(*branch.TreeID)
	branchID := sqldb.MustParseUUID(*branch.BranchID)
	if request.Success {
		row := &sqldb.HistoryTreeRow{
			TreeID:     treeID,
			BranchID:   branchID,
			InProgress: false,
		}
		result, err := m.db.UpdateHistoryTree(row)
		if err != nil {
			return err
		}
		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if rowsAffected != 1 {
			return fmt.Errorf("expected 1 row to be affected for tree table, got %v", rowsAffected)
		}
		return nil
	}
	// request.Success == false
	treeFilter := &sqldb.HistoryTreeFilter{
		TreeID:   treeID,
		BranchID: &branchID,
	}
	nodeFilter := &sqldb.HistoryNodeFilter{
		TreeID:    treeID,
		BranchID:  branchID,
		MinNodeID: common.Int64Ptr(1),
	}
	return m.txExecute("CompleteForkBranch", func(tx sqldb.Tx) error {
		_, err := tx.DeleteFromHistoryNode(nodeFilter)
		if err != nil {
			return err
		}
		result, err := tx.DeleteFromHistoryTree(treeFilter)
		if err != nil {
			return err
		}
		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if rowsAffected != 1 {
			return fmt.Errorf("expected 1 row to be affected for tree table, got %v", rowsAffected)
		}
		return nil
	})
}

// GetHistoryTree returns all branch information of a tree
func (m *sqlHistoryV2Manager) GetHistoryTree(request *p.GetHistoryTreeRequest) (*p.GetHistoryTreeResponse, error) {
	treeID := sqldb.MustParseUUID(request.TreeID)
	branches := make([]*shared.HistoryBranch, 0)
	forkingBranches := make([]p.ForkingInProgressBranch, 0)

	treeFilter := &sqldb.HistoryTreeFilter{
		TreeID: treeID,
	}
	rows, err := m.db.SelectFromHistoryTree(treeFilter)
	if err == sql.ErrNoRows || (err == nil && len(rows) == 0) {
		return &p.GetHistoryTreeResponse{}, nil
	}
	for _, row := range rows {
		if row.InProgress {
			br := p.ForkingInProgressBranch{
				BranchID: row.BranchID.String(),
				ForkTime: row.CreatedTs,
				Info:     row.Info,
			}
			forkingBranches = append(forkingBranches, br)
		}
		ancs, err := m.deserializeAncestors(row.Ancestors)
		if err != nil {
			return nil, err
		}
		br := &shared.HistoryBranch{
			TreeID:    &request.TreeID,
			BranchID:  common.StringPtr(row.BranchID.String()),
			Ancestors: ancs,
		}
		branches = append(branches, br)
	}

	return &p.GetHistoryTreeResponse{
		Branches:                  branches,
		ForkingInProgressBranches: forkingBranches,
	}, nil
}
