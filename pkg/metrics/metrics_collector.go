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

package metrics

import (
	"time"

	dto "github.com/prometheus/client_model/go"
	"go.uber.org/zap"

	"github.com/apache/incubator-yunikorn-core/pkg/log"
	"github.com/apache/incubator-yunikorn-core/pkg/metrics/history"
)

var tickerDefault = 1 * time.Minute

type InternalMetricsCollector struct {
	ticker         *time.Ticker
	stopped        chan bool
	metricsHistory *history.InternalMetricsHistory
}

func NewInternalMetricsCollector(hcInfo *history.InternalMetricsHistory) *InternalMetricsCollector {
	finished := make(chan bool)
	ticker := time.NewTicker(tickerDefault)

	return &InternalMetricsCollector{
		ticker,
		finished,
		hcInfo,
	}
}

func (u *InternalMetricsCollector) StartService() {
	go func() {
		for {
			select {
			case <-u.stopped:
				return
			case <-u.ticker.C:
				log.Logger().Debug("Adding current status to historical partition data")

				totalAppsRunningMetric := &dto.Metric{}
				totalAppsRunningMetricGauge := m.scheduler.getTotalApplicationsRunning()
				err := (*totalAppsRunningMetricGauge).Write(totalAppsRunningMetric)
				if err != nil {
					log.Logger().Warn("Could not encode metric.", zap.Error(err))
					continue
				}

				totalContainersRunningMetric := &dto.Metric{}
				totalContainersRunningMetricCounter := m.scheduler.getAllocatedContainers()
				err = (*totalContainersRunningMetricCounter).Write(totalContainersRunningMetric)
				if err != nil {
					log.Logger().Warn("Could not encode metric.", zap.Error(err))
					continue
				}

				u.metricsHistory.Store(
					int(*totalAppsRunningMetric.Gauge.Value),
					int(*totalContainersRunningMetric.Counter.Value))
			}
		}
	}()
}

func (u *InternalMetricsCollector) Stop() {
	u.stopped <- true
}

func setInternalMetricsCollectorTickerForTest(newDefault time.Duration) {
	tickerDefault = newDefault
}