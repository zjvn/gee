/*
@Copyright : Copyright (C) 2022 THL A29 Limited, a Tencent company. All rights reserved.
@File : geecache.go
@Time : 2023/6/2 11:42
@Author : junerwang@tencent.com
@Desc: this is a simple description...
*/

package geecache

import (
	"fmt"
	pb "geecache/geecachepb"
	"geecache/singleflight"
	"log"
	"sync"
)

// A Getter loads data for a key.
type Getter interface {
	Get(key string) ([]byte, error)
}

// A GetterFunc implements Getter with a function.
type GetterFunc func(key string) ([]byte, error)

// Get implements Getter interface function
func (f GetterFunc) Get(key string) ([]byte, error) {
	return f(key)
}

// A Group is a cache namespace and associated data loaded spread over
type Group struct {
	name      string
	getter    Getter
	mainCache cache
	peers     PeerPicker
	loader    *singleflight.Group // use singleflight.Group to make sure that each key is only fetched once
}

var (
	mu     sync.RWMutex
	groups = make(map[string]*Group)
)

// NewGroup create a new instance of Group
func NewGroup(name string, cacheBytes int64, getter Getter) *Group {
	if getter == nil {
		panic("nil Getter")
	}
	mu.Lock()
	defer mu.Unlock()
	g := &Group{
		name:      name,
		getter:    getter,
		mainCache: cache{cacheBytes: cacheBytes},
		loader:    &singleflight.Group{},
	}
	groups[name] = g
	return g
}

// GetGroup returns the named group previously created with NewGroup, or nil if there's no such group.
func GetGroup(name string) *Group {
	mu.RLock()
	g := groups[name]
	mu.RUnlock()
	return g
}

// RegisterPeers registers a PeerPicker for choosing remote peer
func (g *Group) RegisterPeers(peers PeerPicker) {
	if g.peers != nil {
		panic("RegisterPeerPicker called more than once")
	}
	g.peers = peers
}

func (g *Group) Get(key string) (ByteView, error) {
	if key == "" {
		return ByteView{}, fmt.Errorf("key is required")
	}

	if v, ok := g.mainCache.get(key); ok {
		log.Println("[GeeCache] hit")
		return v, nil
	}

	return g.load(key)
}

func (g *Group) load(key string) (value ByteView, err error) {
	// each key is only fetched once (either locally or remotely)
	// regardless of the number of concurrent callers.
	viewi, err := g.loader.Do(key, func() (interface{}, error) {
		if g.peers != nil {
			if peer, ok := g.peers.PickPeer(key); ok {
				if value, err = g.getFromPeer(peer, key); err == nil {
					return value, nil
				}
				log.Println("[GeeCache] Failed to get from peer", err)
			}
		}

		return g.getLocally(key)
	})

	if err == nil {
		return viewi.(ByteView), nil
	}
	return
}

func (g *Group) getFromPeer(peer PeerGetter, key string) (ByteView, error) {
	req := &pb.Request{
		Group: g.name,
		Key:   key,
	}
	res := &pb.Response{}
	err := peer.Get(req, res)
	if err != nil {
		return ByteView{}, err
	}
	return ByteView{b: res.Value}, nil
}

func (g *Group) getLocally(key string) (ByteView, error) {
	bytes, err := g.getter.Get(key)
	if err != nil {
		return ByteView{}, err
	}
	value := ByteView{b: cloneBytes(bytes)}
	g.populateCache(key, value)
	return value, nil
}

func (g *Group) populateCache(key string, value ByteView) {
	g.mainCache.add(key, value)
}
