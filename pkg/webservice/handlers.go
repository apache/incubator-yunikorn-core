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
package webservice

import (
	"encoding/json"
	"io/ioutil"
	"net/http"
	"runtime"
	"strconv"
	"strings"

	"go.uber.org/zap"

	"github.com/apache/incubator-yunikorn-core/pkg/cache"
	"github.com/apache/incubator-yunikorn-core/pkg/common/configs"
	"github.com/apache/incubator-yunikorn-core/pkg/log"
	"github.com/apache/incubator-yunikorn-core/pkg/webservice/dao"
)

func getStackInfo(w http.ResponseWriter, r *http.Request) {
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func getQueueInfo(w http.ResponseWriter, r *http.Request) {
	writeHeaders(w)

	lists := gClusterInfo.ListPartitions()
	for _, k := range lists {
		partitionInfo := getPartitionJSON(k)

		if err := json.NewEncoder(w).Encode(partitionInfo); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

func getClusterInfo(w http.ResponseWriter, r *http.Request) {
	writeHeaders(w)

	lists := gClusterInfo.ListPartitions()
	for _, k := range lists {
		clusterInfo := getClusterJSON(k)
		var clustersInfo []dao.ClusterDAOInfo
		clustersInfo = append(clustersInfo, *clusterInfo)

		if err := json.NewEncoder(w).Encode(clustersInfo); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

func getApplicationsInfo(w http.ResponseWriter, r *http.Request) {
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func getNodesInfo(w http.ResponseWriter, r *http.Request) {
	writeHeaders(w)

	var result []*dao.NodesDAOInfo
	lists := gClusterInfo.ListPartitions()
	for _, k := range lists {
		var nodesDao []*dao.NodeDAOInfo
		partition := gClusterInfo.GetPartition(k)
		for _, node := range partition.GetNodes() {
			nodeDao := getNodeJSON(node)
			nodesDao = append(nodesDao, nodeDao)
		}
		result = append(result, &dao.NodesDAOInfo{
			PartitionName: partition.Name,
			Nodes:         nodesDao,
		})
	}

	if err := json.NewEncoder(w).Encode(result); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func validateConf(w http.ResponseWriter, r *http.Request) {
	writeHeaders(w)
	requestBytes, err := ioutil.ReadAll(r.Body)
	if err == nil {
		_, err = configs.LoadSchedulerConfigFromByteArray(requestBytes)
	}
	var result dao.ValidateConfResponse
	if err != nil {
		result.Allowed = false
		result.Reason = err.Error()
	} else {
		result.Allowed = true
	}
	if err = json.NewEncoder(w).Encode(result); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func writeHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Credentials", "true")
	w.Header().Set("Access-Control-Allow-Methods", "GET,POST,HEAD,OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "X-Requested-With,Content-Type,Accept,Origin")
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

func getNodeJSON(nodeInfo *cache.NodeInfo) *dao.NodeDAOInfo {
	var allocations []*dao.AllocationDAOInfo
	for _, alloc := range nodeInfo.GetAllAllocations() {
		allocInfo := &dao.AllocationDAOInfo{
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
		allocations = append(allocations, allocInfo)
	}

	return &dao.NodeDAOInfo{
		NodeID:      nodeInfo.NodeID,
		HostName:    nodeInfo.Hostname,
		RackName:    nodeInfo.Rackname,
		Capacity:    strings.Trim(nodeInfo.GetCapacity().String(), "map"),
		Occupied:    strings.Trim(nodeInfo.GetOccupiedResource().String(), "map"),
		Allocated:   strings.Trim(nodeInfo.GetAllocatedResource().String(), "map"),
		Available:   strings.Trim(nodeInfo.GetAvailableResource().String(), "map"),
		Allocations: allocations,
		Schedulable: nodeInfo.IsSchedulable(),
	}
}

func getApplicationHistory(w http.ResponseWriter, r *http.Request) {
	writeHeaders(w)

	// There is nothing to return but we did not really encounter a problem
	if imHistory == nil {
		http.Error(w, "Internal metrics collection is not enabled.", http.StatusNotImplemented)
		return
	}
	var result []*dao.ApplicationHistoryDAOInfo
	// get a copy of the records: if the array contains nil values they will always be at the
	// start and we cannot shortcut the loop using a break, we must finish iterating
	records := imHistory.GetRecords()
	for _, record := range records {
		if record == nil {
			continue
		}
		element := &dao.ApplicationHistoryDAOInfo{
			Timestamp:         record.Timestamp.UnixNano(),
			TotalApplications: strconv.Itoa(record.TotalApplications),
		}
		result = append(result, element)
	}
	if err := json.NewEncoder(w).Encode(result); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func getContainerHistory(w http.ResponseWriter, r *http.Request) {
	writeHeaders(w)

	// There is nothing to return but we did not really encounter a problem
	if imHistory == nil {
		http.Error(w, "Internal metrics collection is not enabled.", http.StatusNotImplemented)
		return
	}
	var result []*dao.ContainerHistoryDAOInfo
	// get a copy of the records: if the array contains nil values they will always be at the
	// start and we cannot shortcut the loop using a break, we must finish iterating
	records := imHistory.GetRecords()
	for _, record := range records {
		if record == nil {
			continue
		}
		element := &dao.ContainerHistoryDAOInfo{
			Timestamp:       record.Timestamp.UnixNano(),
			TotalContainers: strconv.Itoa(record.TotalContainers),
		}
		result = append(result, element)
	}
	if err := json.NewEncoder(w).Encode(result); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}

}
