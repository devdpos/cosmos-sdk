package gov

import (
	"fmt"
	"time"

	"github.com/cosmos/cosmos-sdk/telemetry"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/gov/keeper"
	"github.com/cosmos/cosmos-sdk/x/gov/types"
)

// EndBlocker called every block, process inflation, update validator set.
func EndBlocker(ctx sdk.Context, keeper keeper.Keeper) {
	defer telemetry.ModuleMeasureSince(types.ModuleName, time.Now(), telemetry.MetricKeyEndBlocker)

	logger := keeper.Logger(ctx)

	// delete dead proposals from store and burn theirs deposits. A proposal is dead when it's inactive and didn't get enough deposit on time to get into voting phase.
	keeper.IterateInactiveProposalsQueue(ctx, ctx.BlockHeader().Time, func(proposal types.Proposal) bool {
		keeper.DeleteProposal(ctx, proposal.ProposalId)
		keeper.DeleteAndBurnDeposits(ctx, proposal.ProposalId)

		// called when proposal become inactive
		keeper.AfterProposalFailedMinDeposit(ctx, proposal.ProposalId)

		ctx.EventManager().EmitEvent(
			sdk.NewEvent(
				types.EventTypeInactiveProposal,
				sdk.NewAttribute(types.AttributeKeyProposalID, fmt.Sprintf("%d", proposal.ProposalId)),
				sdk.NewAttribute(types.AttributeKeyProposalResult, types.AttributeValueProposalDropped),
			),
		)

		logger.Info(
			"proposal did not meet minimum deposit; deleted",
			"proposal", proposal.ProposalId,
			"title", proposal.GetTitle(),
			"min_deposit", keeper.GetDepositParams(ctx).MinDeposit.String(),
			"total_deposit", proposal.TotalDeposit.String(),
		)

		return false
	})

	// fetch active proposals whose voting periods have ended (are passed the block time)
	keeper.IterateActiveProposalsQueue(ctx, ctx.BlockHeader().Time, func(proposal types.Proposal) bool {
		var tagValue, logMsg string

		passes, burnDeposits, tallyResults := keeper.Tally(ctx, proposal.ProposalId)

		if burnDeposits {
			keeper.DeleteAndBurnDeposits(ctx, proposal.ProposalId)
		} else {
			keeper.RefundAndDeleteDeposits(ctx, proposal.ProposalId)
		}

		if passes {
			handler := keeper.Handler().GetRoute(proposal.ProposalRoute())
			cacheCtx, writeCache := ctx.CacheContext()

			// The proposal handler may execute state mutating logic depending
			// on the proposal content. If the handler fails, no state mutation
			// is written and the error message is logged.
			err := handler(cacheCtx, proposal.GetContent())
			if err == nil {
				proposal.Status = types.StatusPassed
				tagValue = types.AttributeValueProposalPassed
				logMsg = "passed"

				// The cached context is created with a new EventManager. However, since
				// the proposal handler execution was successful, we want to track/keep
				// any events emitted, so we re-emit to "merge" the events into the
				// original Context's EventManager.
				ctx.EventManager().EmitEvents(cacheCtx.EventManager().Events())

				// write state to the underlying multi-store
				writeCache()
			} else {
				proposal.Status = types.StatusFailed
				tagValue = types.AttributeValueProposalFailed
				logMsg = fmt.Sprintf("passed, but failed on execution: %s", err)
			}
		} else {
			proposal.Status = types.StatusRejected
			tagValue = types.AttributeValueProposalRejected
			logMsg = "rejected"
		}

		proposal.FinalTallyResult = tallyResults

		keeper.SetProposal(ctx, proposal)
		keeper.RemoveFromActiveProposalQueue(ctx, proposal.ProposalId, proposal.VotingEndTime)

		// when proposal become active
		keeper.AfterProposalVotingPeriodEnded(ctx, proposal.ProposalId)

		logger.Info(
			"proposal tallied",
			"proposal", proposal.ProposalId,
			"title", proposal.GetTitle(),
			"result", logMsg,
		)

		ctx.EventManager().EmitEvent(
			sdk.NewEvent(
				types.EventTypeActiveProposal,
				sdk.NewAttribute(types.AttributeKeyProposalID, fmt.Sprintf("%d", proposal.ProposalId)),
				sdk.NewAttribute(types.AttributeKeyProposalResult, tagValue),
			),
		)
		return false
	})

	// For V2 Proposals

	// delete dead proposals from store and burn theirs deposits. A proposal is dead when it's inactive and didn't get enough deposit on time to get into voting phase.
	keeper.IterateInactiveProposalsQueueV2(ctx, ctx.BlockHeader().Time, func(proposal types.ProposalV2) bool {
		keeper.DeleteProposal(ctx, proposal.ProposalId)
		keeper.DeleteAndBurnDeposits(ctx, proposal.ProposalId)

		// called when proposal become inactive
		keeper.AfterProposalFailedMinDeposit(ctx, proposal.ProposalId)

		ctx.EventManager().EmitEvent(
			sdk.NewEvent(
				types.EventTypeInactiveProposal,
				sdk.NewAttribute(types.AttributeKeyProposalID, fmt.Sprintf("%d", proposal.ProposalId)),
				sdk.NewAttribute(types.AttributeKeyProposalResult, types.AttributeValueProposalDropped),
			),
		)

		logger.Info(
			"proposal did not meet minimum deposit; deleted",
			"proposal", proposal.ProposalId,
			"min_deposit", keeper.GetDepositParams(ctx).MinDeposit.String(),
			"total_deposit", proposal.TotalDeposit.String(),
		)

		return false
	})

	// fetch active proposals whose voting periods have ended (are passed the block time)
	keeper.IterateActiveProposalsQueueV2(ctx, ctx.BlockHeader().Time, func(proposal types.ProposalV2) bool {
		var tagValue, logMsg string

		passes, burnDeposits, tallyResults := keeper.Tally(ctx, proposal.ProposalId)

		if burnDeposits {
			keeper.DeleteAndBurnDeposits(ctx, proposal.ProposalId)
		} else {
			keeper.RefundAndDeleteDeposits(ctx, proposal.ProposalId)
		}

		if passes {

			// attempt to execute all messages within the passed proposal
			// Messages may mutate state thus we use a cached context. If one of
			// the handlers fails, no state mutation is written and the error
			// message is logged.
			cacheCtx, writeCache := ctx.CacheContext()
			messages, _ := proposal.GetMessages()
			var (
				err error
				idx int
				msg sdk.Msg
			)
			for idx, msg = range messages {
				handler := keeper.Router().Handler(msg)
				_, err := handler(cacheCtx, msg)
				if err != nil {
					break
				}
			}

			if err == nil {
				proposal.Status = types.StatusPassed
				tagValue = types.AttributeValueProposalPassed
				logMsg = "passed"

				// The cached context is created with a new EventManager. However, since
				// the proposal handler execution was successful, we want to track/keep
				// any events emitted, so we re-emit to "merge" the events into the
				// original Context's EventManager.
				ctx.EventManager().EmitEvents(cacheCtx.EventManager().Events())

				// write state to the underlying multi-store
				writeCache()
			} else {
				proposal.Status = types.StatusFailed
				tagValue = types.AttributeValueProposalFailed
				logMsg = fmt.Sprintf("passed, but msg %d failed on execution: %s", idx, err)
			}
		} else {
			proposal.Status = types.StatusRejected
			tagValue = types.AttributeValueProposalRejected
			logMsg = "rejected"
		}

		proposal.FinalTallyResult = tallyResults

		keeper.SetProposalV2(ctx, proposal)
		keeper.RemoveFromActiveProposalQueue(ctx, proposal.ProposalId, proposal.VotingEndTime)

		// when proposal become active
		keeper.AfterProposalVotingPeriodEnded(ctx, proposal.ProposalId)

		logger.Info(
			"proposal tallied",
			"proposal", proposal.ProposalId,
			"results", logMsg,
		)

		ctx.EventManager().EmitEvent(
			sdk.NewEvent(
				types.EventTypeActiveProposal,
				sdk.NewAttribute(types.AttributeKeyProposalID, fmt.Sprintf("%d", proposal.ProposalId)),
				sdk.NewAttribute(types.AttributeKeyProposalResult, tagValue),
			),
		)
		return false
	})
}
