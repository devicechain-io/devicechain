# Choosing an aggregation window

An aggregation returns one row per bucket per metric. Do the arithmetic before the call, so the request is not refused.

## The limits

- Buckets: the window divided by `intervalSeconds`. At most 10,000.
- Rows: buckets multiplied by the number of metrics that match. At most 50,000. Naming one metric with `name` makes this the bucket count.
- The response: at most 1,000 rows and 256 KiB. This is the limit you will meet first.
- `startTime` and `intervalSeconds` are required. `endTime` may be left out and then means now. Give it anyway, so that two windows you compare have the same length.

So, with one metric named, keep the window divided by the interval at or below 1,000.

## Bucket widths that fit

| Window | Interval | Buckets |
| --- | --- | --- |
| 1 hour | 60 (1 minute) | 60 |
| 6 hours | 300 (5 minutes) | 72 |
| 24 hours | 900 (15 minutes) | 96 |
| 7 days | 3600 (1 hour) | 168 |
| 30 days | 21600 (6 hours) | 120 |
| 90 days | 86400 (1 day) | 90 |

Start coarse. Narrow the window around the change once you have found it, and only then read raw values.

## Reading a bucket

- `name` is the metric's key, the same string a reading shows as its `name`. A result with no buckets at all usually means the key is wrong, not that the device was silent: check the key before reading it as a reporting problem.
- `count` is how many readings arrived in the bucket. A falling count means the device reported less often, whatever the values did.
- An empty bucket is a period with no readings, not a reading of zero.
- Compare like with like: the same bucket width, and the same time of day for a metric with a daily rhythm.

## Raw readings

Read raw values only over a narrow window around a change point, at `pageSize` 100 or less. A page of large text readings can exceed the response limit and is then refused whole. Narrow the window or reduce the page size, and do not retry the same request.
