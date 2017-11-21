package core

import (
	"fmt"
	"github.com/qri-io/dataset/dsfs"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/datatogether/archive"
	"github.com/datatogether/rewrite"
	"github.com/datatogether/warc"
	"github.com/ipfs/go-datastore"
	"github.com/qri-io/cafs"
	"github.com/qri-io/cafs/memfs"
	"github.com/qri-io/dataset"
	"github.com/qri-io/dataset/datatypes"
)

type ArchiveRequests struct {
	Store cafs.Filestore
}

type ArchiveUrlsParams struct {
	Urls         []string
	Parallelism  int
	RequestDelay time.Duration
}

func (r ArchiveRequests) ArchiveUrls(p *ArchiveUrlsParams, path *datastore.Key) error {
	records := warc.Records{}
	recMu := sync.Mutex{}

	archiveUrls := func(urls []string, start, stop int, done chan error) {
		for i := start; i <= stop; i++ {
			rawurl := urls[i]

			if i != start {
				time.Sleep(p.RequestDelay)
			}

			httpreq, err := http.NewRequest("GET", rawurl, nil)
			if err != nil {
				done <- err
				return
			}

			rw := rewrite.NewWarcRecordRewriter(rawurl)
			newrecs, err := archive.ArchiveUrl(httpreq, rw, records)
			if err != nil {
				done <- err
				return
			}

			// check for & remove duplicate resources
			for _, record := range records {
				newrecs = newrecs.RemoveTargetUriRecords(record.TargetUri())
			}
			recMu.Lock()
			// TODO - check for duplicate resources
			records = append(records, newrecs...)
			recMu.Unlock()
		}
		done <- nil
	}

	if len(p.Urls) < p.Parallelism {
		p.Parallelism = len(p.Urls)
	}

	done := make(chan error, p.Parallelism)
	sectionSize := len(p.Urls) / p.Parallelism
	for i := 0; i < p.Parallelism; i++ {
		start := (sectionSize * i)
		stop := (sectionSize * (i + 1)) - 1
		go archiveUrls(p.Urls, start, stop, done)
	}

	for i := 0; i < p.Parallelism; i++ {
		err := <-done // wait for one task to complete
		if err != nil {
			fmt.Println(err.Error())
		}
	}

	pkg, cdxBuf, err := archive.PackageRecords(p.Urls, records)
	if err != nil {
		return err
	}

	indexPath, err := r.Store.Put(memfs.NewMemfileBytes("index.cdxj", cdxBuf.Bytes()), false)
	if err != nil {
		return err
	}

	ds := &dataset.Dataset{
		Title:     "data together web archive",
		Timestamp: time.Now(),
		Structure: &dataset.Structure{
			Format: dataset.CdxjDataFormat,
			Schema: &dataset.Schema{
				Fields: []*dataset.Field{
					&dataset.Field{Name: "surt_uri", Type: datatypes.String},
					&dataset.Field{Name: "timestamp", Type: datatypes.String},
					&dataset.Field{Name: "record_type", Type: datatypes.String},
					&dataset.Field{Name: "metadata", Type: datatypes.String},
				},
			},
		},
		Data: indexPath,
	}

	dsj, err := ds.MarshalJSON()
	if err != nil {
		return err
	}

	pkg.AddChildren(memfs.NewMemfileBytes(dsfs.PackageFileDataset.String(), dsj))

	key, err := r.Store.Put(pkg, false)
	if err != nil {
		return err
	}

	if pinner, ok := r.Store.(cafs.Pinner); ok {
		if err := pinner.Pin(key, true); err != nil {
			return err
		}
	}

	*path = key
	return nil
}

type ArchiveUrlParams struct {
	Method string
	Url    string
	Body   io.Reader
}

func (r ArchiveRequests) ArchiveUrl(p *ArchiveUrlParams, path *datastore.Key) error {
	if p.Method == "" {
		p.Method = "GET"
	}
	httpreq, err := http.NewRequest(p.Method, p.Url, p.Body)
	if err != nil {
		return err
	}

	records := warc.Records{}
	rw := rewrite.NewWarcRecordRewriter(p.Url)
	recs, err := archive.ArchiveUrl(httpreq, rw, records)
	if err != nil {
		return err
	}
	records = append(records, recs...)

	pkg, _, err := archive.PackageRecords([]string{p.Url}, records)
	if err != nil {
		return err
	}

	key, err := r.Store.Put(pkg, false)
	if err != nil {
		return err
	}

	*path = key
	return nil
}
