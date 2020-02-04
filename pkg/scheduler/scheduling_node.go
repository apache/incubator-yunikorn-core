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

package scheduler

import (
	"fmt"
	"strings"
	"sync"

	"go.uber.org/zap"

	"github.com/apache/incubator-yunikorn-core/pkg/cache"
	"github.com/apache/incubator-yunikorn-core/pkg/common/resources"
	"github.com/apache/incubator-yunikorn-core/pkg/log"
	"github.com/apache/incubator-yunikorn-core/pkg/plugins"
	"github.com/apache/incubator-yunikorn-scheduler-interface/lib/go/si"
)

type schedulingNode struct {
	NodeID string

	// Private info
	nodeInfo                    *cache.NodeInfo
	allocating                  *resources.Resource     // resources being allocated
	preempting                  *resources.Resource     // resources considered for preemption
	cachedAvailable             *resources.Resource     // calculated available resources
	cachedAvailableUpdateNeeded bool                    // is the calculated available resource up to date?
	reservations                map[string]*reservation // a map of reservations

	sync.RWMutex
}

func newSchedulingNode(info *cache.NodeInfo) *schedulingNode {
	// safe guard against panic
	if info == nil {
		return nil
	}
	return &schedulingNode{
		nodeInfo:                    info,
		NodeID:                      info.NodeID,
		allocating:                  resources.NewResource(),
		preempting:                  resources.NewResource(),
		cachedAvailableUpdateNeeded: true,
		reservations:                make(map[string]*reservation),
	}
}

// Get the allocated resource on this node.
// These resources are just the confirmed allocations (tracked in the cache node).
// This does not lock the cache node as it will take its own lock.
func (sn *schedulingNode) GetAllocatedResource() *resources.Resource {
	return sn.nodeInfo.GetAllocatedResource()
}

// Get the available resource on this node.
// These resources are confirmed allocations (tracked in the cache node) minus the resources
// currently being allocated but not confirmed in the cache.
// This does not lock the cache node as it will take its own lock.
func (sn *schedulingNode) getAvailableResource() *resources.Resource {
	sn.Lock()
	defer sn.Unlock()
	if sn.cachedAvailableUpdateNeeded {
		sn.cachedAvailable = sn.nodeInfo.GetAvailableResource()
		sn.cachedAvailable.SubFrom(sn.allocating)
		sn.cachedAvailableUpdateNeeded = false
	}
	return sn.cachedAvailable
}

// Get the resource tagged for allocation on this node.
// These resources are part of unconfirmed allocations.
func (sn *schedulingNode) getAllocatingResource() *resources.Resource {
	sn.RLock()
	defer sn.RUnlock()

	return sn.allocating
}

// Update the number of resource proposed for allocation on this node
func (sn *schedulingNode) incAllocatingResource(delta *resources.Resource) {
	sn.Lock()
	defer sn.Unlock()

	sn.cachedAvailableUpdateNeeded = true
	sn.allocating.AddTo(delta)
}

// Handle the allocation processing on the scheduler when the cache node is updated.
func (sn *schedulingNode) decAllocatingResource(delta *resources.Resource) {
	sn.Lock()
	defer sn.Unlock()

	sn.cachedAvailableUpdateNeeded = true
	var err error
	sn.allocating, err = resources.SubErrorNegative(sn.allocating, delta)
	if err != nil {
		log.Logger().Warn("Allocating resources went negative",
			zap.String("nodeID", sn.NodeID),
			zap.Error(err))
	}
}

// Get the number of resource tagged for preemption on this node
func (sn *schedulingNode) getPreemptingResource() *resources.Resource {
	sn.RLock()
	defer sn.RUnlock()

	return sn.preempting
}

// Update the number of resource tagged for preemption on this node
func (sn *schedulingNode) incPreemptingResource(preempting *resources.Resource) {
	sn.Lock()
	defer sn.Unlock()

	sn.preempting.AddTo(preempting)
}

func (sn *schedulingNode) decPreemptingResource(delta *resources.Resource) {
	sn.Lock()
	defer sn.Unlock()
	var err error
	sn.preempting, err = resources.SubErrorNegative(sn.preempting, delta)
	if err != nil {
		log.Logger().Warn("Preempting resources went negative",
			zap.String("nodeID", sn.NodeID),
			zap.Error(err))
	}
}

// Check and update allocating resources of the scheduling node.
// If the proposed allocation fits in the available resources, taking into account resources marked for
// preemption if applicable, the allocating resources are updated and true is returned.
// If the proposed allocation does not fit false is returned and no changes are made.
func (sn *schedulingNode) allocateResource(res *resources.Resource, preemptionPhase bool) bool {
	sn.Lock()
	defer sn.Unlock()
	available := sn.nodeInfo.GetAvailableResource()
	newAllocating := resources.Add(res, sn.allocating)

	if preemptionPhase {
		available.AddTo(sn.preempting)
	}
	// check if this still fits: it might have changed since pre check
	if resources.FitIn(available, newAllocating) {
		log.Logger().Debug("allocations in progress updated",
			zap.String("nodeID", sn.NodeID),
			zap.Any("total unconfirmed", newAllocating))
		sn.cachedAvailableUpdateNeeded = true
		sn.allocating = newAllocating
		return true
	}
	// allocation failed resource did not fit
	return false
}

// Checking allocation conditions in the shim.
// The allocation conditions are implemented via plugins in the shim. If no plugins are
// implemented then the check will return true. If multiple plugins are implemented the first
// failure will stop the checks.
// The caller must thus not rely on all plugins being executed.
// This is a lock free call as it does not change the node and multiple predicate checks could be
// run at the same time.
func (sn *schedulingNode) preAllocateConditions(allocID string) bool {
	// Check the predicates plugin (k8shim)
	if plugin := plugins.GetPredicatesPlugin(); plugin != nil {
		log.Logger().Debug("checking predicates",
			zap.String("allocationId", allocID),
			zap.String("nodeID", sn.NodeID))
		if err := plugin.Predicates(&si.PredicatesArgs{
			AllocationKey: allocID,
			NodeID:        sn.NodeID,
		}); err != nil {
			log.Logger().Debug("running predicates failed",
				zap.String("allocationId", allocID),
				zap.String("nodeID", sn.NodeID),
				zap.Error(err))
			return false
		}
	}
	// all predicate plugins passed
	return true
}

// Check if the node should be considered as a possible node to allocate on
// This is a lock free call. No updates are made this only performs a pre allocate checks
func (sn *schedulingNode) preAllocateCheck(res *resources.Resource, preemptionPhase bool) bool {
	// shortcut if a node is not schedulable
	if !sn.nodeInfo.IsSchedulable() {
		log.Logger().Debug("node is unschedulable",
			zap.String("nodeID", sn.NodeID))
		return false
	}
	// cannot allocate zero or negative resource
	if !resources.StrictlyGreaterThanZero(res) {
		log.Logger().Debug("pre alloc check: requested resource is zero",
			zap.String("nodeID", sn.NodeID))
		return false
	}

	// check if resources are available
	available := sn.nodeInfo.GetAvailableResource()
	if preemptionPhase {
		available.AddTo(sn.preempting)
	}
	newAllocating := resources.Add(res, sn.getAllocatingResource())
	if !resources.FitIn(available, newAllocating) {
		log.Logger().Debug("requested resource is larger than available node resources",
			zap.String("nodeID", sn.NodeID),
			zap.Any("available", available),
			zap.Any("allocating", newAllocating))
		return false
	}
	// can allocate, based on resource size
	return true
}

// Return if the node has been reserved by any application
func (sn *schedulingNode) isReserved() bool {
	sn.RLock()
	defer sn.RUnlock()
	return len(sn.reservations) > 0
}

// Return true if and only if the node has been reserved by the application
// NOTE: a return value of false does not mean the node is not reserved by a different app
func (sn *schedulingNode) isReservedForApp(appID string) bool {
	if appID == "" {
		return false
	}
	sn.RLock()
	defer sn.RUnlock()
	for key := range sn.reservations {
		if strings.HasPrefix(key, appID) {
			return true
		}
	}
	return false
}

// Reserve the node for this application and ask combination, if not reserved yet.
// The reservation is checked against the node resources.
// If the reservation fails the function returns false, if the reservation is made it returns true.
func (sn *schedulingNode) reserve(app *SchedulingApplication, ask *schedulingAllocationAsk) (bool, error) {
	sn.Lock()
	defer sn.Unlock()
	if len(sn.reservations) > 0 {
		return false, fmt.Errorf("node is already reserved, nodeID %s", sn.NodeID)
	}
	appReservation := newReservation(nil, app, ask)
	// this should really not happen just guard against panic
	// either app or ask are nil
	if appReservation == nil {
		log.Logger().Debug("reservation creation failed unexpectedly",
			zap.String("nodeID", sn.NodeID),
			zap.Any("app", app),
			zap.Any("ask", ask))
		return false, fmt.Errorf("reservation creation failed app or ask are nil on nodeID %s", sn.NodeID)
	}
	// reservation must fit on the empty node
	if !sn.nodeInfo.FitInNode(ask.AllocatedResource) {
		log.Logger().Debug("reservation does not fit on the node",
			zap.String("nodeID", sn.NodeID),
			zap.String("appID", app.ApplicationInfo.ApplicationID),
			zap.String("ask", ask.AskProto.AllocationKey),
			zap.String("allocationAsk", ask.AllocatedResource.String()))
		return false, fmt.Errorf("reservation does not fit on node %s, appID %s, ask %s", sn.NodeID, app.ApplicationInfo.ApplicationID, ask.AllocatedResource.String())
	}
	sn.reservations[appReservation.getKey()] = appReservation
	// reservation added successfully
	return true, nil
}

// unReserve the node for this application and ask combination
// If the reservation does not exist it returns false, if the reservation is removed it returns true.
// The error is set if the reservation key cannot be generated.
func (sn *schedulingNode) unReserve(app *SchedulingApplication, ask *schedulingAllocationAsk) (bool, error) {
	sn.Lock()
	defer sn.Unlock()
	resKey := reservationKey(nil, app, ask)
	if resKey == "" {
		log.Logger().Debug("unreserve reservation key create failed unexpectedly",
			zap.String("nodeID", sn.NodeID),
			zap.Any("app", app),
			zap.Any("ask", ask))
		return false, fmt.Errorf("reservation key failed app or ask are nil on nodeID %s", sn.NodeID)
	}
	if _, ok := sn.reservations[resKey]; ok {
		delete(sn.reservations, resKey)
		return true, nil
	}
	// reservation was not found
	log.Logger().Debug("reservation not found while removing from node",
		zap.String("nodeID", sn.NodeID),
		zap.String("appID", app.ApplicationInfo.ApplicationID),
		zap.String("ask", ask.AskProto.AllocationKey))
	return false, nil
}
