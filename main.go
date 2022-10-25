package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rpc"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

var (
	tgBotToken   string
	tgNotiChanID int64
)

type AuRaStepInfo struct {
	Duration            int64
	TransitionStep      int64
	TransitionTimestamp int64
}

func main() {
	rpcUrl := os.Args[1]
	stepsInfo := parseStepsInfo(os.Args[2])
	validatorSet := parseValidatorSet(os.Args[3])
	tgBotToken = os.Args[4]
	var err error
	tgNotiChanID, err = strconv.ParseInt(os.Args[5], 10, 64)
	if err != nil {
		log.Printf("Invalid chat id: %v", err)
		return
	}

	watchBlocks(rpcUrl, stepsInfo, validatorSet)
}

func parseStepsInfo(stepsInfoArg string) []*AuRaStepInfo {
	stepsInfoArgSplit := strings.Split(stepsInfoArg, ",")
	if len(stepsInfoArgSplit) == 0 {
		log.Fatalln("Invalid format for aura steps info")
	}

	stepsInfo := make([]*AuRaStepInfo, 0, len(stepsInfoArgSplit))
	for _, stepInfoArg := range stepsInfoArgSplit {
		stepInfoArgSplit := strings.Split(stepInfoArg, ":")
		if len(stepInfoArgSplit) != 2 {
			log.Fatalln("Invalid format for aura steps info")
		}
		transitionTimestamp, err := strconv.ParseInt(strings.TrimSpace(stepInfoArgSplit[0]), 10, 64)
		if err != nil {
			log.Fatalf("Invalid format for aura steps info: %v\n", err)
		}
		var transitionStep int64
		if len(stepsInfo) > 0 {
			// adjust transition timestamp and compute transition step
			// https://github.com/nethermindeth/nethermind/blob/96767b2574b6d2b702d7f634969aa1b9225c0722/src/Nethermind/Nethermind.Consensus.AuRa/AuRaStepCalculator.cs#L122-L137
			prevStepInfo := stepsInfo[len(stepsInfo)-1]
			prevStepTotalDuration := transitionTimestamp - prevStepInfo.TransitionTimestamp
			prevStepCount := prevStepTotalDuration / prevStepInfo.Duration
			if prevStepTotalDuration%prevStepInfo.Duration > 0 {
				prevStepCount++ // add one step to compensate reminder
			}

			transitionStep = prevStepInfo.TransitionStep + prevStepCount
			transitionTimestamp = prevStepInfo.TransitionTimestamp + prevStepCount*prevStepInfo.Duration
		}

		duration, err := strconv.ParseInt(strings.TrimSpace(stepInfoArgSplit[1]), 10, 64)
		if err != nil {
			log.Fatalf("Invalid format for aura steps info: %v\n", err)
		}

		stepInfo := &AuRaStepInfo{
			Duration:            duration,
			TransitionStep:      transitionStep,
			TransitionTimestamp: transitionTimestamp,
		}
		stepsInfo = append(stepsInfo, stepInfo)
	}

	return stepsInfo
}

func parseValidatorSet(validatorSetArg string) []*common.Address {
	validatorSetArgSplit := strings.Split(validatorSetArg, ",")
	validatorSet := make([]*common.Address, 0, len(validatorSetArgSplit))
	for _, validatorArg := range validatorSetArgSplit {
		validatorAddr := common.HexToAddress(strings.TrimSpace(validatorArg))
		validatorSet = append(validatorSet, &validatorAddr)
	}
	return validatorSet
}

func watchBlocks(rpcUrl string, stepsInfo []*AuRaStepInfo, validatorSet []*common.Address) {
	rpcClient, err := rpc.Dial(rpcUrl)
	if err != nil {
		log.Fatalf("Couldn't dial to rpc endpoint: %v\n", err)
	}

	headCh := make(chan *struct{ Step int64 })
	headSub, err := rpcClient.EthSubscribe(context.Background(), headCh, "newHeads")
	if err != nil {
		log.Fatalf("Couldn't subscribe to new head event: %v\n", err)
	}

	lastStep := int64(-1)

	for {
		select {
		case err := <-headSub.Err():
			log.Fatalf("Subscription to new heads errored: %v\n", err)
		case head := <-headCh:
			if lastStep >= 0 {
				if head.Step > lastStep+1 {
					go reportSkippedSteps(validatorSet, lastStep+1, head.Step)
				} else {
					log.Printf("Received step %v, as expected\n", head.Step)
				}
			}
			lastStep = head.Step
		case <-time.After(time.Minute):
			currStep := getStep(stepsInfo, time.Now().Unix())
			go reportSkippedSteps(validatorSet, lastStep+1, currStep)
		}
	}
}

func reportSkippedSteps(validatorSet []*common.Address, firstSkipped, currStep int64) {
	log.Printf("Reporting skipped steps %v..%v\n", firstSkipped, currStep-1)
	for step := firstSkipped; step < currStep; step++ {
		rawMessage := fmt.Sprintf("Validator %v skipped its step\n", getValidator(validatorSet, step))
		log.Printf(rawMessage)
		if tgBotToken != "" {
			botApi, err := tgbotapi.NewBotAPI(tgBotToken)
			if err != nil {
				log.Printf("Telegram Bot API crashed: %v", err)
				continue
			}
			msg := tgbotapi.NewMessage(tgNotiChanID, rawMessage)
			_, err = botApi.Send(msg)
			if err != nil {
				log.Printf("Failed to send message: %v", err)
				continue
			}
		}
	}
}

func getValidator(validatorSet []*common.Address, step int64) *common.Address {
	return validatorSet[int(step%int64(len(validatorSet)))]
}

func getStep(stepsInfo []*AuRaStepInfo, timestamp int64) int64 {
	firstAbove := sort.Search(len(stepsInfo), func(i int) bool {
		return stepsInfo[i].TransitionTimestamp > timestamp
	})
	stepInfo := stepsInfo[firstAbove-1]
	return stepInfo.TransitionStep + (timestamp-stepInfo.TransitionTimestamp)/stepInfo.Duration
}
