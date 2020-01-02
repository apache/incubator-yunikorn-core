/*
Copyright 2019 Cloudera, Inc.  All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package webservice

import (
	"encoding/json"
	"net/http"
	"runtime"
	"strconv"
	"strings"

	"go.uber.org/zap"

	"github.com/cloudera/yunikorn-core/pkg/cache"
	"github.com/cloudera/yunikorn-core/pkg/log"
	"github.com/cloudera/yunikorn-core/pkg/webservice/dao"
)

func GetStackInfo(w http.ResponseWriter, r *http.Request) {
	writeHeaders(w)
	var stack = func() []byte {
		buf := make([]byte, 1024)
		for {
			n := runtime.Stack(buf, true)
			if n < len(buf) {
				return buf[:n]
			}
			buf = make([]byte, 2*len(buf))
		}
	}
	if _, err := w.Write(stack()); err != nil {
		log.Logger().Error("GetStackInfo error", zap.Error(err))
	}
}

func GetQueueInfo(w http.ResponseWriter, r *http.Request) {
	writeHeaders(w)

	lists := gClusterInfo.ListPartitions()
	for _, k := range lists {
		partitionInfo := getPartitionJSON(k)

		if err := json.NewEncoder(w).Encode(partitionInfo); err != nil {
			panic(err)
		}
	}
}

func GetClusterInfo(w http.ResponseWriter, r *http.Request) {
	writeHeaders(w)

	lists := gClusterInfo.ListPartitions()
	for _, k := range lists {
		clusterInfo := getClusterJSON(k)
		var clustersInfo []dao.ClusterDAOInfo
		clustersInfo = append(clustersInfo, *clusterInfo)

		if err := json.NewEncoder(w).Encode(clustersInfo); err != nil {
			panic(err)
		}
	}
}

func GetApplicationsInfo(w http.ResponseWriter, r *http.Request) {
	writeHeaders(w)

	var appsDao []*dao.ApplicationDAOInfo
	lists := gClusterInfo.ListPartitions()
	for _, k := range lists {
		partition := gClusterInfo.GetPartition(k)
		appList := partition.GetApplications()
		for _, app := range appList {
			appDao := getApplicationJSON(app)
			appsDao = append(appsDao, appDao)
		}
	}

	if err := json.NewEncoder(w).Encode(appsDao); err != nil {
		panic(err)
	}
}

func writeHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Credentials", "true")
	w.Header().Set("Access-Control-Allow-Methods", "GET,POST,HEAD,OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "X-Requested-With,Content-Type,Accept,Origin")
	w.WriteHeader(http.StatusOK)
}

func getClusterJSON(name string) *dao.ClusterDAOInfo {
	clusterInfo := &dao.ClusterDAOInfo{}
	partitionContext := gClusterInfo.GetPartition(name)
	clusterInfo.TotalApplications = strconv.Itoa(partitionContext.GetTotalApplicationCount())
	clusterInfo.TotalContainers = strconv.Itoa(partitionContext.GetTotalAllocationCount())
	clusterInfo.TotalNodes = strconv.Itoa(partitionContext.GetTotalNodeCount())
	clusterInfo.ClusterName = "kubernetes"

	clusterInfo.RunningApplications = strconv.Itoa(partitionContext.GetTotalApplicationCount())
	clusterInfo.RunningContainers = strconv.Itoa(partitionContext.GetTotalAllocationCount())
	clusterInfo.ActiveNodes = strconv.Itoa(partitionContext.GetTotalNodeCount())

	return clusterInfo
}

func getPartitionJSON(name string) *dao.PartitionDAOInfo {
	partitionInfo := &dao.PartitionDAOInfo{}

	partitionContext := gClusterInfo.GetPartition(name)
	queueDAOInfo := partitionContext.GetQueueInfos()

	partitionInfo.PartitionName = partitionContext.Name
	partitionInfo.Capacity = dao.PartitionCapacity{
		Capacity:     partitionContext.GetTotalPartitionResource().String(),
		UsedCapacity: "0",
	}
	partitionInfo.Queues = queueDAOInfo

	return partitionInfo
}

func getApplicationJSON(app *cache.ApplicationInfo) *dao.ApplicationDAOInfo {
	var allocationInfos []dao.AllocationDAOInfo
	allocations := app.GetAllAllocations()
	for _, alloc := range allocations {
		allocInfo := dao.AllocationDAOInfo{
			AllocationKey:    alloc.AllocationProto.AllocationKey,
			AllocationTags:   alloc.AllocationProto.AllocationTags,
			UUID:             alloc.AllocationProto.UUID,
			ResourcePerAlloc: strings.Trim(alloc.AllocatedResource.String(), "map"),
			Priority:         alloc.AllocationProto.Priority.String(),
			QueueName:        alloc.AllocationProto.QueueName,
			NodeID:           alloc.AllocationProto.NodeID,
			ApplicationID:    alloc.AllocationProto.ApplicationID,
			Partition:        alloc.AllocationProto.PartitionName,
		}
		allocationInfos = append(allocationInfos, allocInfo)
	}

	return &dao.ApplicationDAOInfo{
		ApplicationID:  app.ApplicationID,
		UsedResource:   strings.Trim(app.GetAllocatedResource().String(), "map"),
		Partition:      app.Partition,
		QueueName:      app.QueueName,
		SubmissionTime: app.SubmissionTime,
		Allocations:    allocationInfos,
		State:          app.GetApplicationState(),
	}
}
