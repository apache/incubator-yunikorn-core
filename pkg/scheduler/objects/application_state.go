/*
 Licensed to the Apache Software Foundation (ASF) under one
 or more contributor license agreements.  See the NOTICE file
 distributed with this work for additional information
 regarding copyright ownership.  The ASF licenses this file
 to you under the Apache License, Version 2.0 (the
 "License"); you may not use this file except in compliance
 with the License.  You may obtain a copy of the License at

     http://www.apache.org/licenses/LICENSE-2.0

 Unless required by applicable law or agreed to in writing, software
 distributed under the License is distributed on an "AS IS" BASIS,
 WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 See the License for the specific language governing permissions and
 limitations under the License.
*/

package objects

import (
	"fmt"
	"github.com/looplab/fsm"
	"go.uber.org/zap"

	"github.com/apache/incubator-yunikorn-core/pkg/log"
	"github.com/apache/incubator-yunikorn-core/pkg/metrics"
)

const noTransition = "no transition"

// ----------------------------------
// application events
// ----------------------------------
type applicationEvent int

const (
	RunApplication applicationEvent = iota
	WaitApplication
	RejectApplication
	CompleteApplication
	KillApplication
	DeleteApplication
)

func (ae applicationEvent) String() string {
	return [...]string{"RunApplication", "WaitApplication", "RejectApplication", "CompleteApplication", "KillApplication", "DeleteApplication"}[ae]
}

// ----------------------------------
// application states
// ----------------------------------
type applicationState int

const (
	New applicationState = iota
	Accepted
	Starting
	Running
	Waiting
	Rejected
	Completed
	killed
	Deleting
)

func (as applicationState) String() string {
	return [...]string{"New", "Accepted", "Starting", "Running", "Waiting", "Rejected", "Completed", "killed", "Deleting"}[as]
}

func NewAppState() *fsm.FSM {
	return fsm.NewFSM(
		New.String(), fsm.Events{
			{
				Name: RejectApplication.String(),
				Src:  []string{New.String()},
				Dst:  Rejected.String(),
			}, {
				Name: RunApplication.String(),
				Src:  []string{New.String()},
				Dst:  Accepted.String(),
			}, {
				Name: RunApplication.String(),
				Src:  []string{Accepted.String()},
				Dst:  Starting.String(),
			}, {
				Name: RunApplication.String(),
				Src:  []string{Running.String(), Starting.String(), Waiting.String()},
				Dst:  Running.String(),
			}, {
				Name: CompleteApplication.String(),
				Src:  []string{Running.String(), Starting.String(), Waiting.String()},
				Dst:  Completed.String(),
			}, {
				Name: WaitApplication.String(),
				Src:  []string{Accepted.String(), Running.String(), Starting.String()},
				Dst:  Waiting.String(),
			}, {
				Name: KillApplication.String(),
				Src:  []string{Accepted.String(), killed.String(), New.String(), Running.String(), Starting.String(), Waiting.String()},
				Dst:  killed.String(),
			}, {
				Name: DeleteApplication.String(),
				Src:  []string{Completed.String()},
				Dst:  Deleting.String(),
			},
		},
		fsm.Callbacks{
			"enter_state": func(event *fsm.Event) {
				app, ok := event.Args[0].(*Application)
				if !ok {
					log.Logger().Warn("The first argument is not an Application")
					return
				}
				log.Logger().Debug("Application state transition",
					zap.String("appID", app.ApplicationID),
					zap.String("source", event.Src),
					zap.String("destination", event.Dst),
					zap.String("event", event.Event))
				app.OnStateChange(event)
			},
			fmt.Sprintf("enter_%s", Starting.String()): func(event *fsm.Event) {
				event.Args[0].(*Application).SetStateTimer()
			},
			fmt.Sprintf("leave_%s", Starting.String()): func(event *fsm.Event) {
				event.Args[0].(*Application).ClearStateTimer()
			},
			fmt.Sprintf("enter_%s", Waiting.String()): func(event *fsm.Event) {
				event.Args[0].(*Application).SetStateTimer()
			},
			fmt.Sprintf("leave_%s", Waiting.String()): func(event *fsm.Event) {
				event.Args[0].(*Application).ClearStateTimer()
			},
			fmt.Sprintf("leave_%s", New.String()): func(event *fsm.Event) {
				metrics.GetSchedulerMetrics().IncTotalApplicationsAdded()
			},
			fmt.Sprintf("enter_%s", Rejected.String()): func(event *fsm.Event) {
				metrics.GetSchedulerMetrics().IncTotalApplicationsRejected()
			},
			fmt.Sprintf("enter_%s", Running.String()): func(event *fsm.Event) {
				metrics.GetSchedulerMetrics().IncTotalApplicationsRunning()
			},
			fmt.Sprintf("leave_%s", Running.String()): func(event *fsm.Event) {
				metrics.GetSchedulerMetrics().DecTotalApplicationsRunning()
			},
			fmt.Sprintf("enter_%s", Completed.String()): func(event *fsm.Event) {
				metrics.GetSchedulerMetrics().IncTotalApplicationsCompleted()
				event.Args[0].(*Application).SetStateTimer()
			},
		},
	)
}
