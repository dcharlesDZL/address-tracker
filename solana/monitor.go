package solana

import (
	"address-tracker/config"
	"address-tracker/db"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
)

type TxType int

const (
	// tx type
	newBuy = iota
	buy
	sell
	sellAll
)

var NoSigError = errors.New("no signature")

type TxInfo struct {
	Type   TxType
	Owner  string
	Token  string
	Amount float64
}

type WalletGroupSubscription struct {
	Wallet  string
	GroupId int64
	Conn    *websocket.Conn
}
type WSManager struct {
	mu          sync.RWMutex
	connections map[string]*websocket.Conn // identity -> conn
}
type Monitor struct {
	TelegramBot           *tgbotapi.BotAPI
	DBClient              *db.Client
	WebsocketEndpoint     string
	HttpEndpoint          string
	walletInfoMap         map[string]*db.WalletInfo
	walletGroupMap        map[string][]int64
	walletSubscriptionMap map[string]int // wallet address -> subscription id
	Wallets               []string
	allGroups             []int64
	//ConnectionManager     *WSManager // wallet
	WSConnPool *websocket.Conn
	sigCh      chan string
	txCh       chan *TxJSONRPCResponse // tx signature
}

type AddressMention struct {
	Mentions   []string `json:"mentions,omitempty"`
	Commitment string   `json:"commitment,omitempty"`
}
type SubscribeMessage struct {
	Jsonrpc string           `json:"jsonrpc"`
	ID      int              `json:"id"`
	Method  string           `json:"method"`
	Params  []AddressMention `json:"params"`
}

type LogSubscribeResult struct {
	Jsonrpc string `json:"jsonrpc"`
	Result  int    `json:"result"`
	ID      int    `json:"id"`
}

type LogSubscribeResponse struct {
	Jsonrpc string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  struct {
		Result struct {
			Context struct {
				Slot int `json:"slot"`
			} `json:"context"`
			Value struct {
				Signature string      `json:"signature"`
				Err       interface{} `json:"err"`
				Logs      []string    `json:"logs"`
			} `json:"value"`
		} `json:"result"`
		Subscription int `json:"subscription"`
	} `json:"params"`
}

type OwnerSig struct {
	Owner     string
	Signature string
}

type SubscriptionResult struct {
	Jsonrpc string `json:"jsonrpc"`
	Result  int    `json:"result"`
	Id      int    `json:"id"`
}

type TxJSONRPCResponse struct {
	Result  *Result `json:"result"`
	Jsonrpc string  `json:"jsonrpc"`
	ID      int     `json:"id"`
}

type Result struct {
	BlockTime int64        `json:"blockTime"`
	Meta      *Meta        `json:"meta"`
	Slot      int          `json:"slot"`
	Tx        *Transaction `json:"transaction"`
}

type Transaction struct {
	Msg        *Message `json:"message"`
	Signatures []string `json:"signatures"`
}

type Message struct {
	AccountKeys []string `json:"accountKeys"`
	Header      struct {
		NumReadonlySignedAccounts   int `json:"numReadonlySignedAccounts"`
		NumReadonlyUnsignedAccounts int `json:"numReadonlyUnsignedAccounts"`
		NumRequiredSignatures       int `json:"numRequiredSignatures"`
	} `json:"header"`
	RecentBlockhash string `json:"recentBlockhash"`
}

type Meta struct {
	PostTokenBalances []PostTokenBalance `json:"postTokenBalances"`
	PreTokenBalances  []PreTokenBalance  `json:"preTokenBalances"`
}
type PreTokenBalance struct {
	AccountIndex  int           `json:"accountIndex"`
	Mint          string        `json:"mint"`
	Owner         string        `json:"owner"`
	ProgramID     string        `json:"programId"`
	UITokenAmount UITokenAmount `json:"uiTokenAmount"`
}

type PostTokenBalance struct {
	AccountIndex  int           `json:"accountIndex"`
	Mint          string        `json:"mint"`
	Owner         string        `json:"owner"`
	ProgramID     string        `json:"programId"`
	UITokenAmount UITokenAmount `json:"uiTokenAmount"`
}

type UITokenAmount struct {
	Amount         string  `json:"amount"`
	Decimals       int     `json:"decimals"`
	UIAmount       float64 `json:"uiAmount"`
	UIAmountString string  `json:"uiAmountString"`
}

func NewMonitor(cfg *config.Config) (*Monitor, error) {
	bot, err := tgbotapi.NewBotAPI(cfg.TelegramBotAPI)
	if err != nil {
		logrus.Fatal(err)
	}
	dbClient, err := db.NewDbClient(&db.Config{
		Username:     cfg.DBUser,
		Password:     cfg.DBPassword,
		Host:         cfg.DBHost,
		Port:         cfg.DBPort,
		DatabaseName: cfg.DBName,
	})
	if err != nil {
		logrus.Fatalf("init database client error: %v", err)
	}
	//wsManager := NewWSManager()
	return &Monitor{
		TelegramBot:           bot,
		DBClient:              dbClient,
		WebsocketEndpoint:     cfg.SolanaRPCWebsocket,
		HttpEndpoint:          cfg.SolanaRPCHttp,
		walletInfoMap:         make(map[string]*db.WalletInfo),
		walletGroupMap:        make(map[string][]int64),
		walletSubscriptionMap: make(map[string]int),
		Wallets:               nil,
		allGroups:             []int64{},
		//ConnectionManager:     wsManager,
		sigCh:      make(chan string, 10),
		txCh:       make(chan *TxJSONRPCResponse),
		WSConnPool: nil,
	}, nil
}

func (m *Monitor) HandleCommand(message *tgbotapi.Message) error {
	command := message.Command()

	switch command {
	case "addWallet":
		groupId := message.Chat.ID
		args := message.CommandArguments()
		wallet, nickname, _ := splitAddressNickname(args)
		logrus.Info(args)
		// check wallet address type, only support solana and ethereum wallet
		walletChainType := getAddressType(wallet)
		err := m.DBClient.AddWallet(wallet, walletChainType, nickname, groupId)
		if err != nil {
			logrus.Error(err)
			return err
		}
		replyMessage := tgbotapi.NewMessage(message.Chat.ID, "✅Subscribe successfully!")
		m.TelegramBot.Send(replyMessage)
	case "delWallet":
		groupId := message.Chat.ID
		wallet := message.CommandArguments()
		err := m.DBClient.DelWallet(wallet, groupId)
		if err != nil {
			logrus.Error(err)
			return err
		}
		replyMessage := tgbotapi.NewMessage(message.Chat.ID, "✅Unsubscribe successfully!")
		m.TelegramBot.Send(replyMessage)
	//case "aaETH":
	//	// address, nickname, chain, groupID
	//	// postgres
	//	dbConfig := &db.Config{
	//		Username:     dbUser,
	//		Password:     dbPassword,
	//		Host:         dbHost,
	//		Port:         uint64(dbPort),
	//		DatabaseName: dbName,
	//	}
	//	client, err := db.NewDbClient(dbConfig)
	//	if err != nil {
	//		logrus.Error(err)
	//	}
	//	groupId := message.Chat.ID
	//	wallet, nickname, _ := splitAddressNickname(args)
	//
	//	// check wallet address type, only support solana and ethereum wallet
	//	walletChainType := getAddressType(wallet)
	//	err = client.AddWallet(wallet, walletChainType, nickname, groupId)
	//	if err != nil {
	//		logrus.Error(err)
	//	}
	default:
		replyMessage := tgbotapi.NewMessage(message.Chat.ID, "Unknown command. Try /addWallet [address]:[nickname] /delWallet [address].")
		m.TelegramBot.Send(replyMessage)
	}
	return nil
}

func splitAddressNickname(arg string) (string, string, error) {
	parts := strings.Split(arg, ":")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid input, expected format 'ADDRESS:NICKNAME'")
	}
	return parts[0], parts[1], nil
}

func isEthereumAddress(address string) bool {
	matched, _ := regexp.MatchString(`^0x[0-9a-fA-F]{40}$`, address)
	return matched
}

func isSolanaAddress(address string) bool {
	if len(address) != 44 {
		return false
	}
	base58Chars := "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	for _, char := range address {
		if !strings.Contains(base58Chars, string(char)) {
			return false
		}
	}
	return true
}

func getAddressType(address string) string {
	if isSolanaAddress(address) {
		return "solana"
	} else if isEthereumAddress(address) {
		return "ethereum"
	} else {
		return "unknown"
	}
}

func compareWallets(wallets1, wallets2 []string) (added []string, removed []string) {

	walletsMap1 := make(map[string]struct{})
	walletsMap2 := make(map[string]struct{})

	for _, wallet := range wallets1 {
		walletsMap1[wallet] = struct{}{}
	}

	for _, wallet := range wallets2 {
		walletsMap2[wallet] = struct{}{}
	}

	for _, wallet := range wallets2 {
		if _, found := walletsMap1[wallet]; !found {
			added = append(added, wallet)
		}
	}

	for _, wallet := range wallets1 {
		if _, found := walletsMap2[wallet]; !found {
			removed = append(removed, wallet)
		}
	}

	return added, removed
}

func NewWSManager() *WSManager {
	return &WSManager{
		connections: make(map[string]*websocket.Conn),
	}
}

func (m *WSManager) addConnection(identity string, conn *websocket.Conn) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.connections[identity] = conn
	fmt.Printf("Connection added for identity: %s\n", identity)
}

func (m *WSManager) removeConnection(identity string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if conn, exists := m.connections[identity]; exists {
		conn.Close()
		delete(m.connections, identity)
		fmt.Printf("Connection removed for identity: %s\n", identity)
	} else {
		fmt.Printf("No connection found for identity: %s\n", identity)
	}
}

func (m *WSManager) getConnection(identity string) (*websocket.Conn, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	conn, exists := m.connections[identity]
	return conn, exists
}

func (m *WSManager) getAllConnections() map[string]*websocket.Conn {
	m.mu.RLock()
	defer m.mu.RUnlock()
	connsCopy := make(map[string]*websocket.Conn)
	for k, v := range m.connections {
		connsCopy[k] = v
	}
	return connsCopy
}

func (m *Monitor) NewWsConnection() (*websocket.Conn, error) {
	conn, _, err := websocket.DefaultDialer.Dial(m.WebsocketEndpoint, nil)
	if err != nil {
		logrus.Fatalf("webSocket connection error: %v", err)
	}
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(20 * time.Second) // 每 20 秒发送一次 Ping
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				err := conn.WriteMessage(websocket.PingMessage, nil)
				if err != nil {
					logrus.Println("write Ping error:", err)
					return
				}
				logrus.Println("Sent Ping")
			case <-done:
				return
			}
		}
	}()
	conn.SetCloseHandler(m.wsClosehandler)
	return conn, nil
}

func (m *Monitor) wsClosehandler(code int, text string) error {
	logrus.Printf("WebSocket connection closed: code=%d, reason=%s", code, text)
	if code == websocket.CloseAbnormalClosure {
		logrus.Info("Attempting to reconnect...")
		err := m.reconnect()
		if err != nil {
			logrus.Errorf("Reconnect failed: %v", err)
			return err
		} else {
			logrus.Info("Reconnected successfully")
			return nil
		}
	}
	return nil
}
func (m *Monitor) reconnect() error {
	if m.WSConnPool != nil {
		m.WSConnPool.Close()
	}
	logrus.Info("Trying to reconnect...")
	conn, _, err := websocket.DefaultDialer.Dial(m.WebsocketEndpoint, nil)
	if err != nil {
		return fmt.Errorf("failed to reconnect: %v", err)
	}

	m.WSConnPool = conn
	return nil
}

// MonitorAddress monitor all wallets in database
func (m *Monitor) MonitorAddress() {

	//var err error
	//m.Wallets, err = m.DBClient.GetWallets("solana")
	//
	//var addresses []string

	//walletChatgroupMap := make(map[string][]int64)
	//wsConnMap := make(map[*websocket.Conn][]string)

	var err error
	m.WSConnPool, err = m.NewWsConnection()
	if err != nil {
		logrus.Fatalf("create websocket error: %v", err)
	}

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	go func() {
		for {
			select {
			case <-ticker.C:
				allWalletSubscriptions, err := m.DBClient.GetWalletSubscriptions("solana")
				if err != nil {
					logrus.Errorf("get wallets error: %v", err)
				}
				m.updateAddressGroup(allWalletSubscriptions)
				var allWallets []string
				for wallet := range m.walletGroupMap {
					allWallets = append(allWallets, wallet)
				}
				added, removed := compareWallets(m.Wallets, allWallets)
				if len(added) == 0 && len(removed) == 0 {
					continue
				}
				if len(added) > 0 {
					for _, wallet := range added {
						m.subscribe(wallet)
						// if err != nil {
						// 	logrus.Error("subscribe wallet error: ", err)
						// }
						// m.walletSubscriptionMap[wallet] = subId
					}
				}
				// if len(removed) > 0 {
				// 	for _, wallet := range removed {
				// 		m.unsubscribe(wallet)
				// 	}
				// }
				m.Wallets = allWallets
			}
		}
	}()

	go m.receive()
	m.notify()
}

func (m *Monitor) notify() {
	for {
		select {
		case data := <-m.txCh:
			fmt.Println("Received data:", data)
			if data == nil || data.Result == nil {
				logrus.Warn("received nil transaction data")
				continue
			}
			if len(data.Result.Tx.Msg.AccountKeys) == 0 {
				logrus.Warn("transaction has no account keys")
				continue
			}
			owner := data.Result.Tx.Msg.AccountKeys[0]

			txInfo, err := getSwapTxInfo(owner, data)
			if err != nil {
				logrus.Errorf("get tx info error: %v", err)
				continue
			}
			if len(m.walletGroupMap[owner]) == 0 {
				continue
			}
			txText := formatUserActivityText(txInfo, m.walletInfoMap)
			for _, groupId := range m.walletGroupMap[owner] {
				msg := tgbotapi.NewMessage(groupId, txText)
				_, err = m.TelegramBot.Send(msg)
				if err != nil {
					logrus.Fatalf("send message error: %v", err)
				}
			}
		}
	}
}

func (m *Monitor) updateAddressGroup(allWalletSubscriptions []*db.WalletInfo) {
	m.walletGroupMap = make(map[string][]int64)
	m.walletInfoMap = make(map[string]*db.WalletInfo)
	allGroupMap := make(map[int64]bool)
	for _, wallet := range allWalletSubscriptions {
		m.walletInfoMap[wallet.Address] = wallet
		m.walletGroupMap[wallet.Address] = append(m.walletGroupMap[wallet.Address], wallet.GroupId)
		allGroupMap[wallet.GroupId] = true
	}
	for group := range allGroupMap {
		m.allGroups = append(m.allGroups, group)
	}
}

func getTokenSwapSignature(data LogSubscribeResponse) string {
	tokenSwapEvent := "Program TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA"
	for _, logValue := range data.Params.Result.Value.Logs {
		if strings.Contains(logValue, tokenSwapEvent) {
			return data.Params.Result.Value.Signature
		}
	}
	return ""
}

func getIdentity(wallet *db.WalletInfo) string {
	if wallet == nil {
		return ""
	}
	groupIdStr := strconv.FormatInt(wallet.GroupId, 10)
	return wallet.Address + ":" + groupIdStr
}

func (m *Monitor) subscribe(address string) {
	subscribeMessage := fmt.Sprintf(`{"jsonrpc": "2.0","id": 1,"method": "logsSubscribe","params": [{"mentions": ["%s"]},{"commitment": "finalized"}]}`, address)
	if err := m.WSConnPool.WriteMessage(websocket.TextMessage, []byte(subscribeMessage)); err != nil {
		logrus.Fatalf("subscribe error: %v", err)
	}
	logrus.Infof("Successfully subscribe address: %s", address)
	// err := m.WSConnPool.SetReadDeadline(time.Now().Add(5 * time.Second))
	// if err != nil {
	// 	logrus.Error("set read deadline error: ", err)
	// 	return 0, err
	// }
	// _, message, err := m.WSConnPool.ReadMessage()
	// logrus.Info("read sub message: ", string(message))
	// if err != nil {
	// 	logrus.Error("read wallet subscription error: ", err)
	// 	return 0, err
	// }
	// subResult := &SubscriptionResult{}
	// err = json.Unmarshal(message, &subResult)
	// if err != nil {
	// 	logrus.Error("unmarshal subscription result error: ", err)
	// 	return 0, err
	// }
	// return subResult.Result, nil
}

func (m *Monitor) receive() {

	for {
		msgType, message, err := m.WSConnPool.ReadMessage()
		if err != nil {
			logrus.Infof("message type : %d", msgType)
			if msgType != websocket.TextMessage && msgType != websocket.BinaryMessage && msgType != websocket.PingMessage && msgType != websocket.PongMessage {
				if err = m.reconnect(); err != nil {
					logrus.Errorf("recreate websocket connection error: %v", err)
					for _, group := range m.allGroups {
						msg := tgbotapi.NewMessage(group, "websocket connection disconnected, please check.")
						_, err = m.TelegramBot.Send(msg)
						if err != nil {
							logrus.Errorf("send message error: %v", err)
							continue
						}
					}
				}
			}
			logrus.Printf("read message error: %v", err)
			if err = m.reconnect(); err != nil {
				logrus.Errorf("recreate websocket connection error: %v", err)
				for _, group := range m.allGroups {
					msg := tgbotapi.NewMessage(group, "websocket connection disconnected, please check.")
					_, err = m.TelegramBot.Send(msg)
					if err != nil {
						logrus.Errorf("send message error: %v", err)
						continue
					}
				}
			}
			continue
		}
		logrus.Info(string(message))
		var logSubscribeResult LogSubscribeResponse
		if err := json.Unmarshal(message, &logSubscribeResult); err != nil {
			logrus.Printf("unmarshal message error: %v", err)
			continue
		}
		sig := getTokenSwapSignature(logSubscribeResult)

		tx, err := m.getTransaction(sig)
		if err != nil {
			if err == NoSigError {
				logrus.Warn("no signature")
				continue
			}
			logrus.Errorf("get tx error: %v", err)
			continue
		}
		m.txCh <- tx
		logrus.Info(tx)
		// // todo: change another way to save subscription id
		// if tx != nil && len(tx.Result.Tx.Msg.AccountKeys) > 0 {
		// 	owner := tx.Result.Tx.Msg.AccountKeys[0]
		// 	m.walletSubscriptionMap[owner] = logSubscribeResult.Params.Subscription
		// }
	}
}

func (m *Monitor) unsubscribe(address string) {
	unsubscribeMessage := fmt.Sprintf(`
		{
		  "jsonrpc": "2.0",
		  "id": 1,
		  "method": "logsUnsubscribe",
		  "params": [%d]
		}`, m.walletSubscriptionMap[address])
	if err := m.WSConnPool.WriteMessage(websocket.TextMessage, []byte(unsubscribeMessage)); err != nil {
		logrus.Fatalf("unsubscribe error: %v", err)
	}
	logrus.Infof("Unsubscribe Successfully, address: %s", address)
}

func (m *Monitor) getTransaction(sig string) (*TxJSONRPCResponse, error) {
	if sig == "" {
		return nil, NoSigError
	}
	method := "POST"
	payload := strings.NewReader(fmt.Sprintf(`{
    "jsonrpc": "2.0",
    "id": 1,
    "method": "getTransaction",
    "params": [
      "%s",
      {
          "maxSupportedTransactionVersion":0
      }
    ]
  }`, sig))

	client := &http.Client{}
	req, err := http.NewRequest(method, m.HttpEndpoint, payload)

	if err != nil {
		return nil, err
	}
	req.Header.Add("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	txData := &TxJSONRPCResponse{}
	err = json.Unmarshal(body, &txData)
	if err != nil {
		return nil, err
	}
	return txData, nil

}

func getSwapTxInfo(owner string, txData *TxJSONRPCResponse) (*TxInfo, error) {
	if txData == nil {
		return nil, errors.New("tx data is nil, please check")
	}
	if txData.Result.Meta == nil || (txData.Result.Meta.PreTokenBalances == nil && txData.Result.Meta.PostTokenBalances == nil) ||
		(len(txData.Result.Meta.PreTokenBalances) == 0 && len(txData.Result.Meta.PostTokenBalances) == 0) {
		return nil, errors.New("not a token swap tx, please check")
	}
	var token string
	var ownerPreTokenBalance, ownerPostTokenBalance float64
	for _, pre := range txData.Result.Meta.PreTokenBalances {
		if pre.Owner == owner {
			token = pre.Mint
			ownerPreTokenBalance = pre.UITokenAmount.UIAmount
		}
	}
	for _, post := range txData.Result.Meta.PostTokenBalances {
		if post.Owner == owner {
			token = post.Mint
			ownerPostTokenBalance = post.UITokenAmount.UIAmount
		}
	}

	txType := checkTxType(ownerPreTokenBalance, ownerPostTokenBalance)
	amount := calcSwapAmount(ownerPreTokenBalance, ownerPostTokenBalance)
	return &TxInfo{
		Type:   txType,
		Owner:  owner,
		Token:  token,
		Amount: amount,
	}, nil
}

func checkTxType(preBalance, postBalance float64) TxType {
	if preBalance == 0.0 && postBalance > preBalance {
		return newBuy
	} else if preBalance > 0.0 && postBalance > preBalance {
		return buy
	} else if preBalance > postBalance && postBalance == 0.0 {
		return sellAll
	} else if preBalance > postBalance && postBalance > 0.0 {
		return sell
	}
	return -1
}

func calcSwapAmount(preBalance, postBalance float64) float64 {
	if preBalance == 0.0 && postBalance > preBalance {
		return postBalance
	} else if preBalance > 0.0 && postBalance > preBalance {
		return postBalance - preBalance
	} else if preBalance > postBalance && postBalance == 0.0 {
		return preBalance
	} else if preBalance > postBalance && postBalance > 0.0 {
		return preBalance - postBalance
	}
	return 0.0
}

func formatUserActivityText(txInfo *TxInfo, walletMap map[string]*db.WalletInfo) string {
	action := ""
	if txInfo.Type == newBuy {
		action = "新买入"
	} else if txInfo.Type == buy {
		action = "买入"
	} else if txInfo.Type == sell {
		action = "卖出"
	} else if txInfo.Type == sellAll {
		action = "全卖"
	}

	msg := fmt.Sprintf("监控到： %s \n钱包： %s \n代币： %s \n买卖行为： %s \n数量： %.4f", walletMap[txInfo.Owner].Nickname, txInfo.Owner, txInfo.Token, action, txInfo.Amount)
	return msg
}
