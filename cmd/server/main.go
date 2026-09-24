// 边沿译码台：逻辑分析波形、I2C/SPI 交易与重叠候选。
// 空库启动时自动载入离线案例包，无需账号、云端或额外配置。
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"

	"edgebench/internal/casepack"
	"edgebench/internal/store"
	"edgebench/internal/web"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:5358", "listen address")
	dbPath := flag.String("db", "edgebench.db", "sqlite database path")
	flag.Parse()

	st, err := store.Open(*dbPath)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

	max, err := st.MaxSeq()
	if err != nil {
		log.Fatalf("read seq: %v", err)
	}
	if max == 0 {
		if err := casepack.Load(st); err != nil {
			log.Fatalf("load casepack: %v", err)
		}
		log.Print("空库：已载入离线案例包（边沿译码台）")
	}

	srv := web.New(st)
	fmt.Printf("边沿译码台复核页: http://%s\n", *addr)
	log.Fatal(http.ListenAndServe(*addr, srv.Handler()))
}
