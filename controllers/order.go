package controllers

import (
	"github.com/astaxie/beego"
	"github.com/astaxie/beego/orm"
	"dailyFresh/models"
	"strconv"
	"github.com/gomodule/redigo/redis"
	"time"
	"strings"
	"github.com/smartwalle/alipay"
	"github.com/KenmyZhang/aliyun-communicate"
	"encoding/json"
)

type OrderController struct {
	beego.Controller
}

func(this*OrderController)ShowOrder(){
	userName := this.GetSession("userName")
	if userName == nil{
		this.Data["userName"] = ""
	}else{
		this.Data["userName"] = userName.(string)
	}

	this.TplName = "place_order.html"
}
func(this*OrderController)HandleShowOrder(){
	//获取数据
	ids := this.GetStrings("select")
	beego.Info("ids：",ids)
	//校验数据
	if len(ids) == 0{
		beego.Error("传输数据错误")
		return
	}
	//处理数据
	//1.获取地址信息
	userName := this.GetSession("userName")
	o := orm.NewOrm()
	//获取当前用户所有地址信息
	var adds []models.Address
	o.QueryTable("Address").RelatedSel("User").Filter("User__UserName",userName.(string)).All(&adds)
	this.Data["adds"] = adds
	//2.支付方式
	var goods []map[string]interface{}
	conn,err :=redis.Dial("tcp","127.0.0.1:6379")
	if err != nil{
		beego.Error("redis链接失败")
		return
	}
	defer conn.Close()

	//3.获取商品信息和商品数量
	totalCount := 0
	totalPrice := 0

	i := 1
	for _,id := range ids{
		skuid,_ :=strconv.Atoi(id)
		temp := make(map[string]interface{})
		//获取商品信息
		var goodsSku models.GoodsSKU
		goodsSku.Id = skuid
		o.Read(&goodsSku)

		temp["goodsSku"] = goodsSku

		//获取商品数量
		resp,err :=conn.Do("hget","cart_"+userName.(string),skuid)
		count,_ :=redis.Int(resp,err)
		temp["count"] = count
		//计算商品小计
		littlePrice := goodsSku.Price * count
		temp["littlePrice"] = littlePrice
		totalCount += 1
		totalPrice += littlePrice

		temp["i"]  = i
		i +=1

		goods = append(goods,temp)
	}

	//定义运费
	transPrice := 10
	truePrice := transPrice + totalPrice
	this.Data["totalCount"] = totalCount
	this.Data["totalPrice"] = totalPrice
	this.Data["transPrice"] = transPrice
	this.Data["truePrice"] = truePrice

	//返回数据
	this.Data["ids"] = ids
	this.Data["goods"] = goods
	this.TplName = "place_order.html"
}

//处理添加订单业务
func(this*OrderController)HandleAddOrder(){
	resp := make(map[string]interface{})
	defer AJAXRESP(&this.Controller,resp)


	//获取数据
	addrId,err1 :=this.GetInt("addId")
	skuids := this.GetString("skuids")
	payId,err2 := this.GetInt("payId")
	totalCount ,err3 := this.GetInt("totalCount")
	totalPrice,err4 := this.GetInt("totalPrice")
	transPrice,err5 := this.GetInt("transPrice")

	//校验数据u
	if err1 != nil ||  err2 != nil || err3 != nil || err4 != nil || err5 != nil{
		resp["errno"] = 1
		resp["errmsg"] = "传输数据错误"
		return
	}

	//beego.Info("addrId=",addrId,"   skuids=",skuids,"    payId=",payId,"   totalCount=",totalCount, "   totalPrice=",totalPrice,"   transPrice = ",transPrice)
	//处理数据

	//1.把获取到的数据插入到订单表
	o := orm.NewOrm()

	var orderInfo models.OrderInfo
	//插入地址信息
	var addr models.Address
	addr.Id = addrId
	o.Read(&addr)
	orderInfo.Address = &addr

	//插入用户信息
	var user models.User
	userName := this.GetSession("userName")
	user.UserName = userName.(string)
	o.Read(&user,"UserName")
	orderInfo.User = &user

	orderInfo.TransitPrice = transPrice
	orderInfo.TotalPrice = totalPrice
	orderInfo.TotalCount = totalCount
	orderInfo.PayMethod = payId
	orderInfo.OrderId = time.Now().Format("20060102150405"+strconv.Itoa(user.Id))

	//插入
	o.Begin()

	_,err := o.Insert(&orderInfo)
	if err != nil{
		resp["errno"] = 3
		resp["errmsg"] = "订单表插入失败"
	}
	//对商品Id做处理   [1   3    6    8]   字符串  string
	ids :=strings.Split(skuids[1:len(skuids)-1]," ")
	conn,err := redis.Dial("tcp","127.0.0.1:6379")
	if err != nil{
		resp["errno"] = 2
		resp["errmsg"] = "redis连接诶错误"
		return
	}
	defer conn.Close()

	var history_Stock int   //原有库存量

	for _,id := range ids{
		skuid,_ :=strconv.Atoi(id)
		var goodsSku models.GoodsSKU
		goodsSku.Id = skuid
		for i:=0;i<3;i++{
			o.Read(&goodsSku)
			history_Stock = goodsSku.Stock


			//获取商品数量
			re,err :=conn.Do("hget","cart_"+userName.(string),skuid)
			count,_ :=redis.Int(re,err)

			var orderGoods models.OrderGoods
			orderGoods.GoodsSKU = &goodsSku
			orderGoods.Price = count * goodsSku.Price
			orderGoods.Count = count
			orderGoods.OrderInfo = &orderInfo
			if goodsSku.Stock < count{
				resp["errno"] = 4
				resp["errmsg"] = goodsSku.Name+"库存不足"
				o.Rollback()
				return
			}
			o.Insert(&orderGoods)

			//time.Sleep(time.Second * 10)

			if history_Stock != goodsSku.Stock{
				if i == 2 {
					resp["errno"] = 6
					resp["errmsg"] = "商品数量被改变，请重新选择商品"
					o.Rollback()
					return
				}else{
					continue
				}
			}else{
				goodsSku.Stock -= count
				goodsSku.Sales += count
				_,err=o.Update(&goodsSku)
				if err!= nil{
					resp["errno"] = 7
					resp["errmsg"] = "更新错误"
					return
				}
				conn.Do("hdel","cart_"+userName.(string),skuid)
				break
			}
		}
	}

	//给容器赋值
	resp["errno"] = 5
	resp["errmsg"] = "OK"
	////把容器传递给前段
	//this.Data["json"] = resp
	////告诉前端以json格式接受
	//this.ServeJSON()
	//2.把购物车中的数据清除
	o.Commit()

	//返回数据
}

//给支付宝发消息
func(this*OrderController)SendAliPay(){
	var aliPublicKey = `MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAzRI4IxAzr1vhhtv1
Ciy0gRRf8FRBYdJR+PWuvrF5jUmpkvhWWORHJkF0teit7pns8zFoxpxJO3uzbYEyI
Bg6wTAdg6Ci16jXhJPOphu0lSYZ1A3SqhR1O78RPOq4N1bsE76foEdCBpWe3KgM4WK
9/+iF9yVYXaelN4sghR8UqOnn7kTkStqIRL5RDepLN89m7aQXxdfl8vaVbwP1si+Ma
9W+9uwXNUNWGIqFoPdD4q1IJPbPslJSqufGnIDyZGgyyB0Q8I6w5UmrNkno3aOfiu9
iKTUrcbnHQThjSB97w9vLeC5MbhpgAl7YwpDwvbhd3YqDjYF6XCD9nvrW7QoxuQIDAQAB`

	 // 可选，支付宝提供给我们用于签名验证的公钥，通过支付宝管理后台获取
	var privateKey = `MIIEowIBAAKCAQEAwYpCF1Ufk0DV0mRNRZySZbwpmBcnh+Pu7xR6XBmBKiHc+v3Y
YYvJ4ebsKdkUJKc9iHD9LCPrqZSz+21svE5vAmw0fNAO1lzr7YyMCFJDfOMqQXsu
Qj85bACY/6tptFR7y16ssoz9mlHaMasW3JBDCjjEBEVbhhbdZ7RufvfOlmBczQU/
j5cpYdaf6i+sAEaKwgqc1XR6ahsHGAhKPXoA8Ky4DlonxQowO/W76gkSMqhWAcgV
h0MhNchEIh0s7+nqPwUFGBYtRMuf49ZNTUL2HT5ME3B2v9i46jUSbMMbPTaSz7NU
wF2uIOyH8pCHVxRZVwFLikdJ5CT69VJqvt4W1wIDAQABAoIBAHT2Txa2tMxS5GWv
hBtLkhW1bxWg+JzhHOaTY5cBOtPxfxCYFApvZmQFIDfyHoBAKampTvc8BhGH8nVC
HfJ3HBNEvTuoqS7XHSWESKRGws5YopLMFJqohtVETzJDry/x1paC8q89EY4PZWOa
18gXzswAnkVOfQ8+BjPEEPreW5T4PhEtoTsNz0oix02mJN4G/CM/S20xOCoMRANO
2J3QNUXFx319sf8n8N0ls+21mKlwMEEXxh1wmg06YJxHp/SJIGi55PjSbl3Mj3gp
c+0c/oecfdYjs3OPvObFzF9qTFw+jWSRzIXkyboyRrdDkCGpk59ntfeqMX08oxmj
/pYGbqECgYEA6IcLeq8fWZ/cRLUcJ/bTZX+ynJbgBRSWTlbC63js1vINMnWeOUna
Ir1CYVPX3w/hsBSODv7LNegOHqQ5Qe87N3r6PIyoeyxmcJqGHfXhSOGpq5ILA1zN
ZM3NhcXD6S26ocE9v9LMVpjOGN3LnOwIrpKaOlwrwgf4bpCiHVjMC98CgYEA1RO0
BgAU4F6k1Sk5tnPRHP9BKrvDmHZdMIQ8SGLknEW6xwbNfX2k8QjqfrJP7PWncYy8
oq/7j72LubZa7O13JLigFUbTteT6LU3HteKnmlrbrd5E7Z6eX74EetfyxhnOo99/
4LXD0AwA5uhWDgZofWy4hrt+JVgysxvf9sXD1AkCgYEA3q2c76NPaXvu7Blo2ljE
fzn4KX9PD250tpbd2bSXUwzAWKdMm94+uO/35s7tNx+1aPN2S6PzpS8SfoOUlbDt
S6dIhr3JBxQxEfrZH039rdb1rmmQhGrWA4gXHtmSUPbK+ObfJJlRuEhjbmrQ9/kO
I2gfrG3iNdF+NxvpNCN6XI8CgYAnf9iOiDNWiJT74wGM3hl0y6jT+CzBNaf+13Sp
YpPImHCQdqVfTwxllmaKCBoi7kMVHKbXbdIvik69pZ1jcH32s7cRWqjifkkWXuXX
xOWXCqLQr3SNrCrlyr7f2uppaN1SqZr2GBvtlFwSch2JygxSu/XVHCq9V4VGiLNS
9sRfqQKBgDsnt8SAJMXa+qaYTkcxHrF7xeveydHpbygQIHJVDAhBut694BCb5uH6
rxh4blNBCYvDtBN42dTZrtM4CrhbIil8IUZE4M0yy5yG3WrBo8g/imHelFB4Bura
y2PKUc+1kFrg13WkNqMERVuJES08Jdro9Q4iugvyA3MN+N59qNWk` // 必须，上一步中使用 RSA签名验签工具 生成的私钥

	appId := "2016092800615273"
	var client = alipay.New(appId, aliPublicKey, privateKey, false)
	//获取订单号和总价
	orderId := this.GetString("orderId")
	totalPrice := this.GetString("totalPrice")
	if orderId == "" || totalPrice == ""{
		beego.Error("数据传输错误")
		return
	}



	var p = alipay.AliPayTradePagePay{}
	p.NotifyURL = "http://192.168.42.142:8080/payOK"
	p.ReturnURL = "http://192.168.42.142:8080/payOK"
	p.Subject = "天天生鲜商品支付"
	p.OutTradeNo = orderId
	p.TotalAmount = totalPrice
	p.ProductCode = "FAST_INSTANT_TRADE_PAY"

	var url, err = client.TradePagePay(p)
	if err != nil {
		beego.Error("给支付宝发请求失败",err)
	}

	var payURL = url.String()
	this.Redirect(payURL,302)
}

func(this*OrderController)HandleAli(){
	orderId := this.GetString("out_trade_no")
	tradeNo := this.GetString("trade_no")
	if tradeNo == "" || orderId == ""{
		beego.Info("交易失败")
	}else {
		beego.Info("支付成功")
		o := orm.NewOrm()
		var orderInfo models.OrderInfo
		orderInfo.OrderId = orderId
		err := o.Read(&orderInfo,"OrderId")
		if err == nil{
			orderInfo.Orderstatus = 1
			orderInfo.TradeNo = tradeNo
			o.Update(&orderInfo)
		}
	}
	//订单状态修改

	this.Redirect("/goods/userCenterOrder",302)
}




//发送短信
func(this*OrderController)SendMsg(){
	var (
		gatewayUrl      = "http://dysmsapi.aliyuncs.com/"
		accessKeyId     = "LTAIh83X7bYYTIXw"
		accessKeySecret = "fYSLqA3BI8jNviNhURKT9T9TmHeOuP"
		phoneNumbers    = "15986619789"
		signName        = "天天生鲜"
		templateCode    = "SMS_149101793"
		templateParam   = "{\"code\":\"ainio\"}"
	)

	smsClient := aliyunsmsclient.New(gatewayUrl)
	result, err := smsClient.Execute(accessKeyId, accessKeySecret, phoneNumbers, signName, templateCode, templateParam)
	beego.Info("Got raw response from server:", string(result.RawResponse))
	if err != nil {
		beego.Error("Failed to send Message: " + err.Error())
	}

	//json.Marshal() //作用是把key-value形式数据打包成json格式
	resultJson, err := json.Marshal(result)
	//beego.Info("resulSjon=",resultJson,"     result=",result)
	if err != nil {
		beego.Error(err)
	}
	if result.IsSuccessful() {
		beego.Info("A SMS is sent successfully:", resultJson)
	} else {
		beego.Info("Failed to send a SMS:", resultJson)
	}
}

