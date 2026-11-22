package sqlite

import (
	"strings"
	"time"

	"github.com/YunusEmreAlps/geko/internal/investigation"
)

const groupBucketNS = int64(time.Hour)

func groupBucketFloor(ns int64) int64 {
	q := ns / groupBucketNS
	if ns%groupBucketNS < 0 {
		q--
	}
	return q
}

func groupBucketCeil(ns int64) int64 {
	q := ns / groupBucketNS
	if ns%groupBucketNS > 0 {
		q++
	}
	return q
}

// Whole hours use exact weighted totals. At most two boundary hours use
// individual members; arbitrary windows never require sampling or rounding.
// Distinct versions, topology, assignments and pins form separate partitions,
// allowing mappings and triage state to remain query-time decisions.
func summarizedGroupMatches(f investigation.FailureFilter) (string, []any, bool) {
	if f.Search != "" || f.EvidenceService != "" || f.RunID != "" || f.AttributeDigest != "" {
		return "", nil, false
	}
	if f.From != nil && f.To != nil && groupBucketCeil(f.From.UnixNano()) >= groupBucketFloor(f.To.UnixNano()) {
		return "", nil, false // no whole bucket: use the member time index
	}
	assignment := "CASE WHEN p.has_assignment THEN p.assigned END"
	application := "coalesce(nullif(k.namespace,'')," + assignment + ",sa.application,'')"
	key := "CASE WHEN k.namespace<>'' THEN k.group_key ELSE json_set(k.group_key,'$[0]'," + application + ") END"
	joins := ` FROM failure_group_partitions p JOIN failure_group_keys k ON k.id=p.key_id
 LEFT JOIN service_applications sa ON sa.service_name=k.service_name`
	if f.Service != "" && f.Application != nil {
		joins += " LEFT JOIN service_applications ra ON ra.service_name=p.request_service"
	}
	where := " WHERE p.members>0"
	var filterArgs []any
	if f.Pinned {
		where += " AND p.pinned=1"
	}
	if f.Application != nil {
		where += ` AND EXISTS(SELECT 1 FROM json_each(p.services) member
 LEFT JOIN service_applications ma ON ma.service_name=json_extract(member.value,'$[1]')
 WHERE coalesce(nullif(json_extract(member.value,'$[0]'),''),` + assignment + `,ma.application,'')=?)`
		filterArgs = append(filterArgs, *f.Application)
	}
	if f.Service != "" {
		where += " AND CASE WHEN p.has_root THEN p.request_service ELSE k.service_name END=?"
		filterArgs = append(filterArgs, f.Service)
		if f.Application != nil {
			where += " AND CASE WHEN p.has_root THEN coalesce(nullif(p.request_namespace,'')," + assignment + ",ra.application,'') ELSE " + application + " END=?"
			filterArgs = append(filterArgs, *f.Application)
		}
	}
	if f.HTTPStatus != 0 {
		where += " AND p.request_status=?"
		filterArgs = append(filterArgs, f.HTTPStatus)
	}
	if f.GroupKey != "" {
		where += " AND " + key + "=?"
		filterArgs = append(filterArgs, f.GroupKey)
	}
	full := where
	args := append([]any(nil), filterArgs...)
	var edges []int64
	if f.From != nil {
		ns := f.From.UnixNano()
		full += " AND p.bucket>=?"
		args = append(args, groupBucketCeil(ns))
		if ns%groupBucketNS != 0 {
			edges = append(edges, groupBucketFloor(ns))
		}
	}
	if f.To != nil {
		ns := f.To.UnixNano()
		full += " AND p.bucket<?"
		args = append(args, groupBucketFloor(ns))
		if ns%groupBucketNS != 0 {
			bucket := groupBucketFloor(ns)
			if len(edges) == 0 || edges[0] != bucket {
				edges = append(edges, bucket)
			}
		}
	}
	query := "SELECT " + key + ` AS group_key,p.first_ns,p.last_ns,nullif(p.request_status,0) AS request_status,
 p.sample,p.version,p.members AS weight` + joins + full
	if len(edges) > 0 {
		query += " UNION ALL SELECT " + key + ` AS group_key,m.first_ns,m.last_ns,nullif(p.request_status,0) AS request_status,
 printf('%020d',m.last_ns)||hex(m.trace_id)||hex(m.span_id) AS sample,p.version,1 AS weight` + joins +
			" JOIN failure_group_members m ON m.partition_id=p.id" + where + " AND p.bucket IN (?" + strings.Repeat(",?", len(edges)-1) + ")"
		args = append(args, filterArgs...)
		for _, bucket := range edges {
			args = append(args, bucket)
		}
		if f.From != nil {
			query += " AND m.last_ns>=?"
			args = append(args, f.From.UnixNano())
		}
		if f.To != nil {
			query += " AND m.last_ns<?"
			args = append(args, f.To.UnixNano())
		}
	}
	legacy, legacyArgs := uncachedGroupMatches(f)
	return query + " UNION ALL " + legacy, append(args, legacyArgs...), true
}
