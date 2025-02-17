package db

import (
	"fmt"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/sirupsen/logrus"
	"time"
)

type Config struct {
	Username     string
	Password     string
	Host         string
	Port         uint64
	DatabaseName string
}

type Client struct {
	Conn *sqlx.DB
}

type WalletInfo struct {
	Address  string `db:"address"`
	Chain    string `db:"chain"`
	Nickname string `db:"nickname"`
	GroupId  int64  `db:"group_id"`
}

func NewDbClient(config *Config) (*Client, error) {
	pdqlInfo := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable",
		config.Host, config.Port, config.Username, config.Password, config.DatabaseName)
	db, err := sqlx.Connect("postgres", pdqlInfo)
	if err != nil {
		logrus.Errorf("Connected failed.err:%v\n", err)
		return nil, err
	}

	dbConnectionTimeout := time.NewTimer(15 * time.Second)
	go func() {
		<-dbConnectionTimeout.C
		logrus.Fatalf("timeout while connecting to the database")
	}()
	err = db.Ping()
	if err != nil {
		logrus.Errorf("ping db fail, err: %v", err)
	}

	dbConnectionTimeout.Stop()

	db.SetConnMaxIdleTime(time.Second * 30)
	db.SetConnMaxLifetime(time.Second * 60)
	db.SetMaxOpenConns(200)
	db.SetMaxIdleConns(200)

	logrus.Info("Successfully connected!")
	return &Client{
		Conn: db,
	}, nil
}

func (c *Client) AddWallet(walletAddress, chain, nickname string, groupId int64) error {
	sql := `INSERT INTO addresses(address, chain, nickname, group_id) values ($1, $2, $3, $4);`
	_, err := c.Conn.Exec(sql, walletAddress, chain, nickname, groupId)
	if err != nil {
		logrus.Errorf("add wallet error: %v \n", err)
		return err
	}
	return nil
}

func (c *Client) DelWallet(walletAddress string, groupId int64) error {
	sql := `DELETE FROM addresses WHERE address = $1 AND group_id = $2;`
	_, err := c.Conn.Exec(sql, walletAddress, groupId)
	if err != nil {
		logrus.Errorf("delete wallet error: %v \n", err)
		return err
	}
	return nil
}

func (c *Client) GetWalletSubscriptions(chain string) ([]*WalletInfo, error) {
	var wallets []*WalletInfo
	sql := `select * from addresses where chain = $1;`
	err := c.Conn.Select(&wallets, sql, chain)
	if err != nil {
		logrus.Errorf("get wallet error: %v \n", err)
		return nil, err
	}
	return wallets, nil
}
