// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"fmt"
)

// Page represents a paginated result set.
type Page[T any] struct {
	Items      []T   // The items for current page
	Total      int64 // Total count (if available)
	Page       int   // Current page number (0-indexed)
	PageSize   int   // Items per page
	TotalPages int64 // Calculated total pages
}

// Paginate executes the query with pagination.
// Returns current page, total count, and error.
//
// Example:
//
//	page, err := quark.For[User](ctx, client).Paginate(100, 0) // 100 per page, page 0
//	page, err := quark.For[User](ctx, client).Where("active", "=", true).Paginate(50, 2)
func (q *Query[T]) Paginate(pageSize, page int) (*Page[T], error) {
	if q.client == nil {
		return nil, fmt.Errorf("%w: client not initialized", ErrInvalidQuery)
	}

	if pageSize <= 0 {
		pageSize = 100
	}
	if page < 0 {
		page = 0
	}

	// Clone before mutating to preserve immutability of the original query.
	pq := q.clone()
	pq.limit = pageSize
	pq.hasLimit = true
	pq.offset = page * pageSize

	// Get total count first (without LIMIT/OFFSET so we get the real total)
	total, err := q.Count()
	if err != nil {
		return nil, err
	}

	// Get items for this page
	items, err := pq.List()
	if err != nil {
		return nil, err
	}

	totalPages := (total + int64(pageSize) - 1) / int64(pageSize)
	if totalPages < 1 {
		totalPages = 1
	}

	return &Page[T]{
		Items:      items,
		Total:      total,
		Page:       page,
		PageSize:   pageSize,
		TotalPages: totalPages,
	}, nil
}
