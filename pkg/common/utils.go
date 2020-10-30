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

package common

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/apache/incubator-yunikorn-core/pkg/log"
)

func GetNormalizedPartitionName(partitionName string, rmID string) string {
	if partitionName == "" {
		partitionName = "default"
	}

	// handle already normalized partition name
	if strings.HasPrefix(partitionName, "[") {
		return partitionName
	}
	return fmt.Sprintf("[%s]%s", rmID, partitionName)
}

func GetRMIdFromPartitionName(partitionName string) string {
	idx := strings.Index(partitionName, "]")
	if idx > 0 {
		rmID := partitionName[1:idx]
		return rmID
	}
	return ""
}

func GetPartitionNameWithoutClusterID(partitionName string) string {
	idx := strings.Index(partitionName, "]")
	if idx > 0 {
		return partitionName[idx+1:]
	}
	return partitionName
}

func WaitFor(interval time.Duration, timeout time.Duration, condition func() bool) error {
	deadline := time.Now().Add(timeout)
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for condition")
		}
		if condition() {
			return nil
		}
		time.Sleep(interval)
		continue
	}
}

func GetBoolEnvVar(key string, defaultVal bool) bool {
	if value, ok := os.LookupEnv(key); ok {
		boolValue, err := strconv.ParseBool(value)
		if err != nil {
			log.Logger().Debug("Failed to parse ENV variable, using the default one",
				zap.String("name", key),
				zap.String("value", value),
				zap.Bool("default", defaultVal))
			return defaultVal
		}
		return boolValue
	}
	return defaultVal
}
