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
	"github.com/apache/incubator-yunikorn-core/pkg/common"
	"github.com/apache/incubator-yunikorn-core/pkg/common/commonevents"
	"github.com/apache/incubator-yunikorn-core/pkg/common/resources"
	"github.com/apache/incubator-yunikorn-core/pkg/common/security"
	"github.com/apache/incubator-yunikorn-core/pkg/log"
	"github.com/apache/incubator-yunikorn-core/pkg/scheduler/placement"
)

type partitionSchedulingContext struct {
	RmID string // the RM the partition belongs to
	Name string // name of the partition (logging mainly)

	// Private fields need protection
	partition        *cache.PartitionInfo              // link back to the partition in the cache
	root             *SchedulingQueue                  // start of the scheduling queue hierarchy
	applications     map[string]*SchedulingApplication // applications assigned to this partition
	reservedApps     map[string]int                    // applications reserved within this partition, with reservation count
	nodes            map[string]*SchedulingNode        // nodes assigned to this partition
	placementManager *placement.AppPlacementManager    // placement manager for this partition
	partitionManager *partitionManager                 // manager for this partition

	sync.RWMutex
}

// Create a new partitioning scheduling context.
// the flattened list is generated by a separate call
func newPartitionSchedulingContext(info *cache.PartitionInfo, root *SchedulingQueue) *partitionSchedulingContext {
	if info == nil || root == nil {
		return nil
	}
	psc := &partitionSchedulingContext{
		applications: make(map[string]*SchedulingApplication),
		reservedApps: make(map[string]int),
		nodes:        make(map[string]*SchedulingNode),
		root:         root,
		Name:         info.Name,
		RmID:         info.RmID,
		partition:    info,
	}
	psc.placementManager = placement.NewPlacementManager(info)
	return psc
}

func (psc *partitionSchedulingContext) getPartitionAvailable() *resources.Resource {
	psc.RLock()
	defer psc.RUnlock()

	available := psc.partition.GetTotalPartitionResource()
	available.SubFrom(psc.root.GetAllocatedResource())
	available.SubFrom(psc.root.getAllocatingResource())
	return available
}

// Update the scheduling partition based on the reloaded config.
func (psc *partitionSchedulingContext) updatePartitionSchedulingContext(info *cache.PartitionInfo) {
	psc.Lock()
	defer psc.Unlock()

	if psc.placementManager.IsInitialised() {
		log.Logger().Info("Updating placement manager rules on config reload")
		err := psc.placementManager.UpdateRules(info.GetRules())
		if err != nil {
			log.Logger().Info("New placement rules not activated, config reload failed", zap.Error(err))
		}
	} else {
		log.Logger().Info("Creating new placement manager on config reload")
		psc.placementManager = placement.NewPlacementManager(info)
	}
	root := psc.root
	// update the root queue properties
	root.updateSchedulingQueueProperties(info.Root.GetProperties())
	// update the rest of the queues recursively
	root.updateSchedulingQueueInfo(info.Root.GetCopyOfChildren(), root)
}

// Add a new application to the scheduling partition.
func (psc *partitionSchedulingContext) addSchedulingApplication(schedulingApp *SchedulingApplication) error {
	psc.Lock()
	defer psc.Unlock()

	// Add to applications
	appID := schedulingApp.ApplicationInfo.ApplicationID
	if psc.applications[appID] != nil {
		return fmt.Errorf("adding application %s to partition %s, but application already existed", appID, psc.Name)
	}

	// Put app under the scheduling queue, the app has already been placed in the partition cache
	queueName := schedulingApp.ApplicationInfo.QueueName
	if psc.placementManager.IsInitialised() {
		err := psc.placementManager.PlaceApplication(schedulingApp.ApplicationInfo)
		if err != nil {
			return fmt.Errorf("failed to place app in requested queue '%s' for application %s: %v", queueName, appID, err)
		}
		// pull out the queue name from the placement
		queueName = schedulingApp.ApplicationInfo.QueueName
	}
	// we have a queue name either from placement or direct
	schedulingQueue := psc.getQueue(queueName)
	// check if the queue already exist and what we have is a leaf queue with submit access
	if schedulingQueue != nil &&
		(!schedulingQueue.isLeafQueue() || !schedulingQueue.checkSubmitAccess(schedulingApp.ApplicationInfo.GetUser())) {
		return fmt.Errorf("failed to find queue %s for application %s", schedulingApp.ApplicationInfo.QueueName, appID)
	}
	// with placement rules the hierarchy might not exist so try and create it
	if schedulingQueue == nil {
		psc.createSchedulingQueue(queueName, schedulingApp.ApplicationInfo.GetUser())
		// find the scheduling queue: if it still does not exist we fail the app
		schedulingQueue = psc.getQueue(queueName)
		if schedulingQueue == nil {
			return fmt.Errorf("failed to find queue %s for application %s", schedulingApp.ApplicationInfo.QueueName, appID)
		}
	}

	// all is OK update the app and partition
	schedulingApp.queue = schedulingQueue
	schedulingQueue.addSchedulingApplication(schedulingApp)
	psc.applications[appID] = schedulingApp

	return nil
}

// Remove the application from the scheduling partition.
func (psc *partitionSchedulingContext) removeSchedulingApplication(appID string) (*SchedulingApplication, error) {
	psc.Lock()
	defer psc.Unlock()

	// Remove from applications map
	if psc.applications[appID] == nil {
		return nil, fmt.Errorf("removing application %s from partition %s, but application does not exist", appID, psc.Name)
	}
	schedulingApp := psc.applications[appID]
	delete(psc.applications, appID)
	delete(psc.reservedApps, appID)

	// Remove all asks and thus all reservations and pending resources (queue included)
	queueName := schedulingApp.ApplicationInfo.QueueName
	_ = schedulingApp.removeAllocationAsk("")
	log.Logger().Debug("application removed from the scheduler",
		zap.String("queue", queueName),
		zap.String("applicationID", appID))

	// Remove app from queue
	schedulingQueue := psc.getQueue(queueName)
	if schedulingQueue == nil {
		// This is not normal return an error and log
		log.Logger().Warn("failed to find assigned queue while removing application",
			zap.String("queue", queueName),
			zap.String("applicationID", appID))
		return nil, fmt.Errorf("failed to find queue %s while removing application %s", queueName, appID)
	}
	schedulingQueue.removeSchedulingApplication(schedulingApp)

	return schedulingApp, nil
}

// Return a copy of the map of all reservations for the partition.
// This will return an empty map if there are no reservations.
// Visible for tests
func (psc *partitionSchedulingContext) getReservations() map[string]int {
	psc.RLock()
	defer psc.RUnlock()
	reserve := make(map[string]int)
	for key, num := range psc.reservedApps {
		reserve[key] = num
	}
	return reserve
}

// Get the queue from the structure based on the fully qualified name.
// Wrapper around the unlocked version getQueue()
// Visible by tests
func (psc *partitionSchedulingContext) GetQueue(name string) *SchedulingQueue {
	psc.RLock()
	defer psc.RUnlock()
	return psc.getQueue(name)
}

// Get the queue from the structure based on the fully qualified name.
// The name is not syntax checked and must be valid.
// Returns nil if the queue is not found otherwise the queue object.
//
// NOTE: this is a lock free call. It should only be called holding the PartitionSchedulingContext lock.
func (psc *partitionSchedulingContext) getQueue(name string) *SchedulingQueue {
	// start at the root
	queue := psc.root
	part := strings.Split(strings.ToLower(name), cache.DOT)
	// no input
	if len(part) == 0 || part[0] != "root" {
		return nil
	}
	// walk over the parts going down towards the requested queue
	for i := 1; i < len(part); i++ {
		// if child not found break out and return
		if queue = queue.childrenQueues[part[i]]; queue == nil {
			break
		}
	}
	return queue
}

func (psc *partitionSchedulingContext) getApplication(appID string) *SchedulingApplication {
	psc.RLock()
	defer psc.RUnlock()

	return psc.applications[appID]
}

// Create a scheduling queue with full hierarchy. This is called when a new queue is created from a placement rule.
// It will not return anything and cannot "fail". A failure is picked up by the queue not existing after this call.
//
// NOTE: this is a lock free call. It should only be called holding the PartitionSchedulingContext lock.
func (psc *partitionSchedulingContext) createSchedulingQueue(name string, user security.UserGroup) {
	// find the scheduling furthest down the hierarchy that exists
	schedQueue := name // the scheduling queue that exists
	cacheQueue := ""   // the cache queue that needs to be created (with children)
	parent := psc.getQueue(schedQueue)
	for parent == nil {
		cacheQueue = schedQueue
		schedQueue = name[0:strings.LastIndex(name, cache.DOT)]
		parent = psc.getQueue(schedQueue)
	}
	// found the last known scheduling queue,
	// create the corresponding scheduler queue based on the already created cache queue
	queue := psc.partition.GetQueue(cacheQueue)
	// if the cache queue does not exist we should fail this create
	if queue == nil {
		return
	}
	// Check the ACL before we really create
	// The existing parent scheduling queue is the lowest we need to look at
	if !parent.checkSubmitAccess(user) {
		log.Logger().Debug("Submit access denied by scheduler on queue",
			zap.String("deniedQueueName", schedQueue),
			zap.String("requestedQueue", name))
		return
	}
	log.Logger().Debug("Creating scheduling queue(s)",
		zap.String("parent", schedQueue),
		zap.String("child", cacheQueue),
		zap.String("fullPath", name))
	newSchedulingQueueInfo(queue, parent)
}

// Get a scheduling node from the partition by nodeID.
func (psc *partitionSchedulingContext) getSchedulingNode(nodeID string) *SchedulingNode {
	psc.RLock()
	defer psc.RUnlock()

	return psc.nodes[nodeID]
}

// Get a copy of the scheduling nodes from the partition.
// This list does not include reserved nodes or nodes marked unschedulable
func (psc *partitionSchedulingContext) getSchedulableNodes() []*SchedulingNode {
	return psc.getSchedulingNodes(true)
}

// Get a copy of the scheduling nodes from the partition.
// Excludes unschedulable nodes only, reserved node inclusion depends on the parameter passed in.
func (psc *partitionSchedulingContext) getSchedulingNodes(excludeReserved bool) []*SchedulingNode {
	psc.RLock()
	defer psc.RUnlock()

	schedulingNodes := make([]*SchedulingNode, 0)
	for _, node := range psc.nodes {
		// filter out the nodes that are not scheduling
		if !node.nodeInfo.IsSchedulable() || (excludeReserved && node.isReserved()) {
			continue
		}
		schedulingNodes = append(schedulingNodes, node)
	}
	return schedulingNodes
}

// Add a new scheduling node triggered on the addition of the cache node.
// This will log if the scheduler is out of sync with the cache.
// As a side effect it will bring the cache and scheduler back into sync.
func (psc *partitionSchedulingContext) addSchedulingNode(info *cache.NodeInfo) {
	if info == nil {
		return
	}

	psc.Lock()
	defer psc.Unlock()
	// check consistency and reset to make sure it is consistent again
	if _, ok := psc.nodes[info.NodeID]; ok {
		log.Logger().Debug("new node already existed: cache out of sync with scheduler",
			zap.String("nodeID", info.NodeID))
	}
	// add the node, this will also get the sync back between the two lists
	psc.nodes[info.NodeID] = newSchedulingNode(info)
}

func (psc *partitionSchedulingContext) updateSchedulingNode(info *cache.NodeInfo) {
	if info == nil {
		return
	}

	psc.Lock()
	defer psc.Unlock()
	if schedulingNode, ok := psc.nodes[info.NodeID]; ok {
		schedulingNode.updateNodeInfo(info)
	} else {
		log.Logger().Warn("node is not found in partitionSchedulingContext while attempting to update it",
			zap.String("nodeID", info.NodeID))
	}
}

// Remove a scheduling node triggered by the removal of the cache node.
// This will log if the scheduler is out of sync with the cache.
// Should never be called directly as it will bring the scheduler out of sync with the cache.
func (psc *partitionSchedulingContext) removeSchedulingNode(nodeID string) {
	if nodeID == "" {
		return
	}

	psc.Lock()
	defer psc.Unlock()
	// check consistency just for debug
	node, ok := psc.nodes[nodeID]
	if !ok {
		log.Logger().Debug("node to be removed does not exist: cache out of sync with scheduler",
			zap.String("nodeID", nodeID))
		return
	}
	// remove the node, this will also get the sync back between the two lists
	delete(psc.nodes, nodeID)
	// unreserve all the apps that were reserved on the node
	var reservedKeys []string
	reservedKeys, ok = node.unReserveApps()
	if !ok {
		log.Logger().Warn("Node removal did not remove all application reservations this can affect scheduling",
			zap.String("nodeID", nodeID))
	}
	// update the partition reservations based on the node clean up
	for _, appID := range reservedKeys {
		psc.unReserveCount(appID, 1)
	}
}

// Try regular allocation for the partition
// Lock free call this all locks are taken when needed in called functions
func (psc *partitionSchedulingContext) tryAllocate() *schedulingAllocation {
	if !resources.StrictlyGreaterThanZero(psc.root.GetPendingResource()) {
		// nothing to do just return
		return nil
	}
	// try allocating from the root down
	return psc.root.tryAllocate(psc)
}

// Try process reservations for the partition
// Lock free call this all locks are taken when needed in called functions
func (psc *partitionSchedulingContext) tryReservedAllocate() *schedulingAllocation {
	psc.Lock()
	if len(psc.reservedApps) == 0 {
		psc.Unlock()
		return nil
	}
	psc.Unlock()
	// try allocating from the root down
	return psc.root.tryReservedAllocate(psc)
}

// Process the allocation and make the changes in the partition.
// If the allocation needs to be passed on to the cache true will be returned if not false is returned
func (psc *partitionSchedulingContext) allocate(alloc *schedulingAllocation) bool {
	psc.Lock()
	defer psc.Unlock()
	// partition is locked nothing can change from now on
	// find the app make sure it still exists
	appID := alloc.schedulingAsk.ApplicationID
	app := psc.applications[appID]
	if app == nil {
		log.Logger().Info("Application was removed while allocating",
			zap.String("appID", appID))
		return false
	}
	// find the node make sure it still exists
	// if the node was passed in use that ID instead of the one from the allocation
	// the node ID is set when a reservation is allocated on a non-reserved node
	var nodeID string
	if alloc.reservedNodeID == "" {
		nodeID = alloc.nodeID
	} else {
		nodeID = alloc.reservedNodeID
		log.Logger().Debug("Reservation allocated on different node",
			zap.String("current node", alloc.nodeID),
			zap.String("reserved node", nodeID),
			zap.String("appID", appID))
	}
	node := psc.nodes[nodeID]
	if node == nil {
		log.Logger().Info("Node was removed while allocating",
			zap.String("nodeID", nodeID),
			zap.String("appID", appID))
		return false
	}
	// reservation does not leave the scheduler
	if alloc.result == reserved {
		psc.reserve(app, node, alloc.schedulingAsk)
		return false
	}
	// unreserve does not leave the scheduler
	if alloc.result == unreserved || alloc.result == allocatedReserved {
		// unreserve only in the scheduler
		psc.unReserve(app, node, alloc.schedulingAsk)
		// real allocation after reservation does get passed on to the cache
		if alloc.result == unreserved {
			return false
		}
	}

	log.Logger().Info("scheduler allocation proposal",
		zap.String("appID", alloc.schedulingAsk.ApplicationID),
		zap.String("queue", alloc.schedulingAsk.QueueName),
		zap.String("allocationKey", alloc.schedulingAsk.AskProto.AllocationKey),
		zap.String("allocatedResource", alloc.schedulingAsk.AllocatedResource.String()),
		zap.String("targetNode", alloc.nodeID))
	return true
}

// Confirm the allocation. This is called as the result of the scheduler passing the proposal to the cache.
// This updates the allocating resources for app, queue and node in the scheduler
// Called for both allocations from reserved as well as for direct allocations.
// The unreserve is already handled before we get here so there is no difference in handling.
func (psc *partitionSchedulingContext) confirmAllocation(allocProposal *commonevents.AllocationProposal, confirm bool) error {
	psc.RLock()
	defer psc.RUnlock()
	// partition is locked nothing can change from now on
	// find the app make sure it still exists
	appID := allocProposal.ApplicationID
	app := psc.applications[appID]
	if app == nil {
		return fmt.Errorf("application was removed while allocating: %s", appID)
	}
	// find the node make sure it still exists
	nodeID := allocProposal.NodeID
	node := psc.nodes[nodeID]
	if node == nil {
		return fmt.Errorf("node was removed while allocating app %s: %s", appID, nodeID)
	}

	// Node and app exist we now can confirm the allocation
	allocKey := allocProposal.AllocationKey
	log.Logger().Debug("processing allocation proposal",
		zap.String("partition", psc.Name),
		zap.String("appID", appID),
		zap.String("nodeID", nodeID),
		zap.String("allocKey", allocKey),
		zap.Bool("confirmation", confirm))

	// this is a confirmation or rejection update inflight allocating resources of all objects
	delta := allocProposal.AllocatedResource
	if !resources.IsZero(delta) {
		log.Logger().Debug("confirm allocation: update allocating resources",
			zap.String("partition", psc.Name),
			zap.String("appID", appID),
			zap.String("nodeID", nodeID),
			zap.String("allocKey", allocKey),
			zap.String("delta", delta.String()))
		// update the allocating values with the delta
		app.decAllocatingResource(delta)
		app.queue.decAllocatingResource(delta)
		node.decAllocatingResource(delta)
	}

	if !confirm {
		// The repeat gets "added back" when rejected
		// This is a noop when the ask was removed in the mean time: no follow up needed
		_, err := app.updateAskRepeat(allocKey, 1)
		if err != nil {
			return err
		}
	} else if app.GetSchedulingAllocationAsk(allocKey) == nil {
		// The ask was removed while we processed the proposal. The scheduler is updated but we need
		// to make sure the cache removes the allocation that we cannot confirm
		return fmt.Errorf("ask was removed while allocating for app %s: %s", appID, allocKey)
	}

	// all is ok when we are here
	log.Logger().Info("allocation proposal confirmed",
		zap.String("appID", appID),
		zap.String("allocationKey", allocKey),
		zap.String("nodeID", nodeID))
	return nil
}

// Process the reservation in the scheduler
// Lock free call this must be called holding the context lock
func (psc *partitionSchedulingContext) reserve(app *SchedulingApplication, node *SchedulingNode, ask *schedulingAllocationAsk) {
	appID := app.ApplicationInfo.ApplicationID
	// app has node already reserved cannot reserve again
	if app.isReservedOnNode(node.NodeID) {
		log.Logger().Info("Application is already reserved on node",
			zap.String("appID", appID),
			zap.String("nodeID", node.NodeID))
		return
	}
	// all ok, add the reservation to the app, this will also reserve the node
	if err := app.reserve(node, ask); err != nil {
		log.Logger().Debug("Failed to handle reservation, error during update of app",
			zap.Error(err))
		return
	}

	// add the reservation to the queue list
	app.queue.reserve(appID)
	// increase the number of reservations for this app
	psc.reservedApps[appID]++

	log.Logger().Info("allocation ask is reserved",
		zap.String("appID", ask.ApplicationID),
		zap.String("queue", ask.QueueName),
		zap.String("allocationKey", ask.AskProto.AllocationKey),
		zap.String("node", node.NodeID))
}

// Process the unreservation in the scheduler
// Lock free call this must be called holding the context lock
func (psc *partitionSchedulingContext) unReserve(app *SchedulingApplication, node *SchedulingNode, ask *schedulingAllocationAsk) {
	appID := app.ApplicationInfo.ApplicationID
	if psc.reservedApps[appID] == 0 {
		log.Logger().Info("Application is not reserved in partition",
			zap.String("appID", appID))
		return
	}
	// all ok, remove the reservation of the app, this will also unReserve the node
	if err := app.unReserve(node, ask); err != nil {
		log.Logger().Info("Failed to unreserve, error during allocate on the app",
			zap.Error(err))
		return
	}
	// remove the reservation of the queue
	app.queue.unReserve(appID)
	// make sure we cannot go below 0
	psc.unReserveCount(appID, 1)

	log.Logger().Info("allocation ask is unreserved",
		zap.String("appID", ask.ApplicationID),
		zap.String("queue", ask.QueueName),
		zap.String("allocationKey", ask.AskProto.AllocationKey),
		zap.String("node", node.NodeID))
}

// Get the iterator for the sorted nodes list from the partition.
func (psc *partitionSchedulingContext) getNodeIteratorForPolicy(nodes []*SchedulingNode) NodeIterator {
	// Sort Nodes based on the policy configured.
	configuredPolicy := psc.partition.GetNodeSortingPolicy()
	if configuredPolicy == common.Undefined {
		return nil
	}
	sortNodes(nodes, configuredPolicy)
	return NewDefaultNodeIterator(nodes)
}

// Create a node iterator for the schedulable nodes based on the policy set for this partition.
// The iterator is nil if there are no schedulable nodes available.
func (psc *partitionSchedulingContext) getNodeIterator() NodeIterator {
	if nodeList := psc.getSchedulableNodes(); len(nodeList) != 0 {
		return psc.getNodeIteratorForPolicy(nodeList)
	}
	return nil
}

// Locked version of the reservation counter update
// Called by the scheduler
func (psc *partitionSchedulingContext) unReserveUpdate(appID string, asks int) {
	psc.Lock()
	defer psc.Unlock()
	psc.unReserveCount(appID, asks)
}

// Update the reservation counter for the app
// Lock free call this must be called holding the context lock
func (psc *partitionSchedulingContext) unReserveCount(appID string, asks int) {
	if num, found := psc.reservedApps[appID]; found {
		// decrease the number of reservations for this app and cleanup
		if num == asks {
			delete(psc.reservedApps, appID)
		} else {
			psc.reservedApps[appID] -= asks
		}
	}
}
