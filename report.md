GSLB Package Audit Report
Executive Summary
The pkg/gslb package implements a sophisticated bucket-based timer system for probing. However, there are critical architecture flaws that likely cause the "timing is not working" issues observed by the user. The most significant issues are dropping of probe results under load and blocking database calls in the critical timer path.

Critical Findings
1. Data Loss: Results Dropped Silently
Location: pkg/gslb/bucket_worker_pool.go (line 207) Issue: The worker pool uses a non-blocking send to the shared resultQueue.

select {
case bwp.resultQueue <- result:
    atomic.AddInt64(&bwp.totalProbes, 1)
default:
    bwp.logger.Warnf("Result queue full, dropping result for IP %s", result.IP)
}
Impact: resultQueue is shared across ALL buckets (limit 20,000). If the result processor falls behind (even slightly) or multiple buckets fire simultaneously (e.g., at :00 seconds), the queue fills up. Subsequent probe results are DROPPED. Consequence: The system performs the probe but discards the result. Health state is not updated, counters don't increment, and the user sees "missing" updates or timing gaps.

2. Timer Drift: Blocking DB Calls in Ticker Loop
Location: pkg/gslb/timer_bucket.go (line 255) Issue: The runBucketCycle function runs directly on the Ticker goroutine and makes a synchronous database call:

ipsByRecord, err := tb.ipHealthManager.GetIPsByRecordIDs(tb.ctx, recordIDs)
Impact: If MongoDB is slow (e.g., takes 2s to return 50k IPs), the entire bucket cycle is delayed by 2s. This "drifts" the schedule. Consequence: For 10s intervals, a 2s DB delay + 1s processing could mean the cycle finishes at T+3s. The next tick (at T+10s) waits for this to finish. If processing > 10s, ticks are skipped.

3. Data Loss: Probe Tasks Dropped
Location: pkg/gslb/bucket_worker_pool.go (line 136) Issue: Similar to results, task submission is non-blocking:

select {
case bwp.probeQueue <- task:
    return true
default:
    // Drops task
    return false
}
Impact: If the workers (min/max limits) cannot keep up with the burst of tasks generated at the start of a cycle, the probeQueue fills, and subsequent IPs in that cycle are SKIPPED (not probed at all).

Minor Findings / Risks
4. Shared Result Queue Contention
Location: pkg/gslb/health_checker.go Issue: A single channel resultQueue (size 20k) services all buckets. With 300k endpoints, a simultaneous firing of multiple buckets could generate >50k results instantly, easily overflowing the 20k buffer.

5. Autoscaler Thrashing Potential
Location: pkg/gslb/bucket_worker_pool.go Issue: The autoscaler aggressively scales up (>70% queue) and down (<20%). With bursty traffic (timer bucket dumps all tasks at once), the queue goes 0 -> 100% -> 0 rapidy. The autoscaler might react too slowly or oscillate.

Recommendations
Fix Result Dropping (High Priority):

Change resultQueue send to be BLOCKING (remove select/default). Backpressure should throttle the workers, not drop data.
Alternatively, increase resultQueue size significantly or use per-bucket result queues.
Fix Task Dropping (High Priority):

Make Submit() blocking or use a dynamic/unbounded queue (intermediate buffer). Dropping tasks means "no health check this cycle", which is dangerous.
Optimize Timer Loop:

Move GetIPsByRecordIDs to a separate goroutine or ensure it's heavily optimized (which it seems to be, but network glitches will stall the ticker).
Verify Processor Throughput:

Ensure the 8 processResultsSharded goroutines are sufficient to drain the resultQueue faster than 300k probes can be generated.