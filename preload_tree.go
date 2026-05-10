// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import "strings"

// preloadNode is one node in the dotted-path preload tree.
//
//	q.preloads = []string{"Orders.Items.Product", "Tags"}
//
// parses to:
//
//	[Orders → Items → Product]
//	[Tags]
//
// Each node carries its relation name plus child nodes that fire after the
// relation has been loaded. Phase 2 nested-preload deliverable.
type preloadNode struct {
	name     string
	children []*preloadNode
}

// parsePreloads splits each dotted path into a tree node, merging shared
// prefixes so a Preload("Orders.Items", "Orders.Customer") only loads
// Orders once.
func parsePreloads(paths []string) []*preloadNode {
	root := &preloadNode{}
	for _, path := range paths {
		segs := strings.Split(path, ".")
		insertPreloadPath(root, segs)
	}
	return root.children
}

func insertPreloadPath(parent *preloadNode, segs []string) {
	if len(segs) == 0 {
		return
	}
	head := strings.TrimSpace(segs[0])
	if head == "" {
		return
	}
	var child *preloadNode
	for _, c := range parent.children {
		if c.name == head {
			child = c
			break
		}
	}
	if child == nil {
		child = &preloadNode{name: head}
		parent.children = append(parent.children, child)
	}
	insertPreloadPath(child, segs[1:])
}
