package limiter

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

type Limiter struct{ Client *redis.Client }

func Open(raw string) (*Limiter, error) {
	o, e := redis.ParseURL(raw)
	if e != nil {
		return nil, e
	}
	o.DialTimeout = 3 * time.Second
	o.ReadTimeout = 2 * time.Second
	o.WriteTimeout = 2 * time.Second
	return &Limiter{redis.NewClient(o)}, nil
}

// Both buckets are consumed atomically; Redis TIME avoids host clock skew.
var acquire = redis.NewScript(`
local now=redis.call('TIME'); local ms=now[1]*1000+math.floor(now[2]/1000)
local buckets={}; local wait=0
for i=1,#KEYS do
 local rate=tonumber(ARGV[i]); local v=redis.call('HMGET',KEYS[i],'tokens','at')
 local tokens=tonumber(v[1]) or rate; local at=tonumber(v[2]) or ms
 tokens=math.min(rate,tokens+math.max(0,ms-at)*rate/1000)
 buckets[i]={tokens,rate}
 if tokens<1 then wait=math.max(wait,math.ceil((1-tokens)*1000/rate)) end
end
if wait>0 then return wait end
for i=1,#KEYS do
 redis.call('HSET',KEYS[i],'tokens',buckets[i][1]-1,'at',ms)
 redis.call('PEXPIRE',KEYS[i],120000)
end
return 0
`)

func (l *Limiter) Take(ctx context.Context, tenant, destination string, tenantRate int) (time.Duration, error) {
	// Shared hash tag supports a future Redis Cluster deployment without cross-slot Lua.
	n, e := acquire.Run(ctx, l.Client, []string{"mail:{limits}:tenant:" + tenant, "mail:{limits}:destination:" + destination}, tenantRate, 20).Int64()
	return time.Duration(n) * time.Millisecond, e
}
func (l *Limiter) Ingress(ctx context.Context, tenant string, rate int) (time.Duration, error) {
	n, e := acquire.Run(ctx, l.Client, []string{"mail:{limits}:ingress:" + tenant}, rate).Int64()
	return time.Duration(n) * time.Millisecond, e
}
