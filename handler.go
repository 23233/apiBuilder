package ab

import (
	"fmt"
	jsoniter "github.com/json-iterator/go"
	"github.com/kataras/iris/v12"
	"github.com/kataras/iris/v12/sessions/sessiondb/redis"
	"github.com/pkg/errors"
	"strconv"
	"strings"
	"time"
	"xorm.io/xorm"
)

// 错误返回
func fastError(err error, ctx iris.Context, msg ...string) {
	ctx.StatusCode(iris.StatusBadRequest)
	var m string
	if err == nil {
		m = ctx.Tr("apiParamsParseFail", "请求解析出错")
	} else {
		m = err.Error()
	}
	if len(msg) >= 1 {
		m = msg[0]
	}
	_, _ = ctx.JSON(iris.Map{
		"detail": m,
	})
	return
}

// GetAllFunc 获取所有
// page控制页码 page_size控制条数 最大均为100 100页 100条
// order(asc) order_desc
// search搜索 __会被替换为% search=__赵日天 sql会替换为 %赵日天
// filter_[字段名]进行过滤 filter_id=1
func (c *RestApi) GetAllFunc(ctx iris.Context) {
	model := c.pathGetModel(ctx.Path())
	page := ctx.URLParamIntDefault("page", 1)
	maxCount, maxSize := model.getPage()
	if page > maxCount {
		page = maxCount
	}
	pageSize := ctx.URLParamIntDefault("page_size", 20)
	if pageSize > maxSize {
		pageSize = maxSize
	}

	// 解析出order by
	descField := ctx.URLParam("order_desc")
	orderBy := ctx.URLParam("order")
	// 从url中解析出filter
	filterList := filterMatch(ctx.URLParams(), model.info.FieldList.Fields)
	//
	s := ctx.URLParam("search")
	search := strings.ReplaceAll(s, "__", "%")
	if len(search) >= 1 {
		if len(model.searchFields) < 1 {
			fastError(errors.New("搜索功能未启用"), ctx)
			return
		}
	}

	privateValue := ctx.Values().Get(model.PrivateContextKey)
	start := (page - 1) * pageSize
	end := page * (pageSize * 2)

	var base = func() *xorm.Session {
		var d *xorm.Session
		d = c.C.Mdb.Table(model.info.MapName)
		if model.private {
			d = d.Where(fmt.Sprintf("%s = ?", model.PrivateColName), privateValue)
		}
		if len(orderBy) >= 1 {
			d = d.OrderBy(orderBy)
		} else if len(descField) >= 1 {
			d = d.Desc(descField)
		}
		return d
	}

	where := func() *xorm.Session {
		var d *xorm.Session
		d = base()
		if len(model.info.FieldList.Deleted) >= 1 {
			d = base().Where(fmt.Sprintf("%s = ? OR %s IS NULL", model.info.FieldList.Deleted, model.info.FieldList.Deleted), "0001-01-01 00:00:00")
		}
		if len(filterList) >= 1 {
			for k, v := range filterList {
				d = d.Where(fmt.Sprintf("%s = ?", k), v)
			}
		}
		if len(search) >= 1 {
			for _, s := range model.searchFields {
				d = d.Where(fmt.Sprintf("%s like ?", s), search)
			}
		}
		return d
	}

	// 获取总数量
	allCount, err := where().Count()
	if err != nil {
		fastError(err, ctx)
		return
	}

	// 获取内容
	dataList := make([]map[string]string, 0)
	if allCount >= 1 {
		if len(model.info.FieldList.AutoIncrement) >= 1 && len(filterList) < 1 && len(orderBy) < 1 && len(descField) < 1 && len(search) < 1 {
			dataList, err = where().And(fmt.Sprintf("%s between ? and ?", model.info.FieldList.AutoIncrement), start, end).Limit(pageSize).QueryString()
		} else {
			dataList, err = where().Limit(pageSize, start).QueryString()
		}
		if err != nil {
			fastError(err, ctx)
			return
		}
	}

	// 需要转换返回值
	if model.allResp.Has && len(dataList) > 0 {
		r := make([]map[string]string, 0, len(dataList))
		for _, item := range dataList {
			c := map[string]string{}
			for k, v := range item {
				// 遍历字段名
				for _, field := range model.allResp.Fields {
					if field.MapName == k {
						c[k] = v
						break
					}
				}
			}
			r = append(r, c)
		}
		dataList = r
	}

	result := iris.Map{
		"page_size": pageSize,
		"page":      page,
		"all":       allCount,
		"data":      dataList,
	}
	if len(descField) >= 1 {
		result["desc_field"] = descField
	}
	if len(orderBy) >= 1 {
		result["order"] = orderBy
	}
	if len(filterList) >= 1 {
		result["filter"] = filterList
	}
	if len(search) >= 1 {
		result["search"] = s
	}

	// 如果启用了缓存
	if model.getAllListCacheTime() >= 1 {
		// 生成key
		rKey := genRedisKey(ctx.Request().RequestURI, model.PrivateColName, fmt.Sprintf("%v", privateValue))
		// 保存结果
		resp, err := jsoniter.MarshalToString(result)
		if err != nil {
			// todo redis错误处理
		}
		err = c.saveToRedis(ctx.Request().Context(), rKey, resp, model.getAllListCacheTime())
		if err != nil {
			// todo redis保存错误处理
		}
	}

	_, _ = ctx.JSON(result)
}

// GetSingle 单个 /{id:uint64}
func (c *RestApi) GetSingle(ctx iris.Context) {
	id, err := ctx.Params().GetUint64("id")
	if err != nil {
		fastError(err, ctx)
		return
	}
	model := c.pathGetModel(ctx.Path())
	privateValue := ctx.Values().Get(model.PrivateContextKey)
	newData := c.newModel(model.info.MapName)

	var base = func() *xorm.Session {
		if model.private {
			return c.C.Mdb.Table(newData).Where(fmt.Sprintf("%s = ?", model.PrivateColName), privateValue)
		}
		return c.C.Mdb.Table(newData)
	}
	has, err := base().ID(id).Get(newData)
	if err != nil || has == false {
		fastError(err, ctx, ctx.Tr("apiNotFoundDataFail", "查询数据失败"))
		return
	}

	// 需要转换返回值
	if model.singleResp.Has {
		n := c.newType(model.singleResp.Instance)
		_ = Replace(newData, n)
		newData = n
	}

	// 如果启用了缓存
	if model.getSingleCacheTime() >= 1 {
		// 生成key
		rKey := genRedisKey(ctx.Request().RequestURI, model.PrivateColName, fmt.Sprintf("%v", privateValue))
		// 保存结果
		resp, err := jsoniter.MarshalToString(newData)
		if err != nil {
			// todo redis错误处理
		}
		err = c.saveToRedis(ctx.Request().Context(), rKey, resp, model.getSingleCacheTime())
		if err != nil {
			// todo redis保存错误处理
		}
	}

	_, _ = ctx.JSON(newData)
}

// AddData 新增数据
func (c *RestApi) AddData(ctx iris.Context) {
	model := c.pathGetModel(ctx.Path())
	newInstance, err := c.getCtxValues(model.info.MapName, ctx)
	if err != nil {
		fastError(err, ctx)
		return
	}
	if model.private {
		privateName := ctx.Values().Get(model.PrivateContextKey)
		private := newInstance.Elem().FieldByName(model.PrivateColName)
		c := fmt.Sprintf("%v", privateName)
		switch private.Type().String() {
		case "string":
			private.SetString(c)
			break
		case "int", "int8", "int16", "int32", "int64", "time.Duration":
			i, _ := strconv.Atoi(c)
			private.SetInt(int64(i))
			break
		case "uint", "uint8", "uint16", "uint32", "uint64":
			i, _ := strconv.Atoi(c)
			private.SetUint(uint64(i))
			break
		default:
			fastError(err, ctx, ctx.Tr("apiPrivateParseFail", "私密参数解析错误"))
			return
		}
	}

	singleData := newInstance.Interface()

	aff, err := c.C.Mdb.Table(model.info.MapName).InsertOne(singleData)
	if err != nil || aff == 0 {
		fastError(err, ctx, ctx.Tr("apiAddDataFail", "新增数据失败"))
		return
	}
	// 需要转换返回值
	if model.postResp.Has {
		n := c.newType(model.postResp.Instance)
		_ = Replace(singleData, n)
		singleData = n
	}

	_, _ = ctx.JSON(singleData)
}

// EditData 编辑数据 /{id:uint64}
func (c *RestApi) EditData(ctx iris.Context) {
	model := c.pathGetModel(ctx.Path())
	privateValue := ctx.Values().Get(model.PrivateContextKey)
	id, err := ctx.Params().GetUint64("id")
	if err != nil {
		fastError(err, ctx)
		return
	}

	var base = func() *xorm.Session {
		if model.private {
			return c.C.Mdb.Table(model.info.MapName).Where(fmt.Sprintf("%s = ?", model.PrivateColName), privateValue)
		}
		return c.C.Mdb.Table(model.info.MapName)
	}
	// 先获取数据是否存在
	has, err := base().Where("id = ?", id).Exist()
	if err != nil {
		fastError(err, ctx)
		return
	}
	if has != true {
		fastError(err, ctx, ctx.Tr("apiNotFoundDataFail", "查询数据失败"))
		return
	}
	newInstance, err := c.getCtxValues(model.info.MapName, ctx)
	if err != nil {
		fastError(err, ctx)
		return
	}

	if model.private {
		private := newInstance.Elem().FieldByName(model.PrivateColName)
		c := fmt.Sprintf("%v", privateValue)
		switch private.Type().String() {
		case "string":
			private.SetString(c)
			break
		case "int", "int8", "int16", "int32", "int64", "time.Duration":
			i, _ := strconv.Atoi(c)
			private.SetInt(int64(i))
			break
		case "uint", "uint8", "uint16", "uint32", "uint64":
			i, _ := strconv.Atoi(c)
			private.SetUint(uint64(i))
			break
		default:
			fastError(err, ctx, ctx.Tr("apiPrivateParseFail", "私密参数解析错误"))
			return
		}
	}

	// 更新之前判断是否启用缓存
	if model.getSingleCacheTime() >= 1 {
		// 删除缓存
		rKey := genRedisKey(ctx.Request().RequestURI, model.PrivateColName, fmt.Sprintf("%v", privateValue))
		err := c.deleteToRedis(ctx.Request().Context(), rKey)
		if err != nil {
			// todo redis删除缓存出错
		}
	}

	// 全量更新
	singleData := newInstance.Interface()
	aff, err := c.C.Mdb.Table(model.info.MapName).ID(id).AllCols().Update(singleData)
	if err != nil || aff < 1 {
		fastError(err, ctx, ctx.Tr("apiUpdateFail", "更新数据失败"))
		return
	}

	// 再次删除缓存 双删确保安全
	if model.getSingleCacheTime() >= 1 {
		go func() {
			time.Sleep(model.getDelayDeleteTime())
			// 再次删除缓存 不保证结果
			rKey := genRedisKey(ctx.Request().RequestURI, model.PrivateColName, fmt.Sprintf("%v", privateValue))
			_ = c.deleteToRedis(ctx.Request().Context(), rKey)
		}()
	}

	// 需要转换返回值
	if model.putResp.Has {
		n := c.newType(model.putResp.Instance)
		_ = Replace(singleData, n)
		_, _ = ctx.JSON(n)
		return
	}

	_, _ = ctx.JSON(iris.Map{"id": id})
}

// DeleteData 删除数据 /{id:uint64}
func (c *RestApi) DeleteData(ctx iris.Context) {
	// 先获取
	model := c.pathGetModel(ctx.Path())
	privateValue := ctx.Values().Get(model.PrivateContextKey)
	id, err := ctx.Params().GetUint64("id")
	newData := c.newModel(model.info.MapName)

	if err != nil {
		fastError(err, ctx)
		return
	}
	var base = func() *xorm.Session {
		if model.private {
			return c.C.Mdb.Table(newData).Where(fmt.Sprintf("%s = ?", model.PrivateColName), privateValue)
		}
		return c.C.Mdb.Table(newData)
	}
	// 先获取数据是否存在

	has, err := base().ID(id).Get(newData)
	if err != nil {
		fastError(err, ctx)
		return
	}
	if has != true {
		fastError(err, ctx, ctx.Tr("apiNotFoundData", "获取数据失败"))
		return
	}
	// 进行删除
	aff, err := base().ID(id).Delete(newData)
	if err != nil || aff < 1 {
		fastError(err, ctx, ctx.Tr("apiDeleteFail", "删除数据失败"))
		return
	}

	// 删除key
	if model.getSingleCacheTime() >= 1 {
		// 删除缓存
		rKey := genRedisKey(ctx.Request().RequestURI, model.PrivateColName, fmt.Sprintf("%v", privateValue))
		err := c.deleteToRedis(ctx.Request().Context(), rKey)
		if err != nil {
			// todo redis删除缓存出错
		}
	}

	// 需要转换返回值
	if model.deleteResp.Has {
		n := c.newType(model.deleteResp.Instance)
		_ = Replace(newData, n)
		_, _ = ctx.JSON(n)
		return
	}
	_, _ = ctx.JSON(iris.Map{"id": id})

}

// 获取数据的中间件
func (c *RestApi) getCacheMiddleware(ctx iris.Context) {
	model := c.pathGetModel(ctx.Path())
	// 判断header中 Cache-control
	cacheHeader := ctx.GetHeader("Cache-control")
	if cacheHeader == "no-cache" {
		ctx.Next()
		return
	}
	privateValue := ctx.Values().Get(model.PrivateContextKey)
	// 获取参数 生成key
	rKey := genRedisKey(ctx.Request().RequestURI, model.PrivateColName, fmt.Sprintf("%v", privateValue))
	// 获取缓存内容
	resp, err := c.C.Rdb.Get(ctx.Request().Context(), rKey).Result()
	if err != nil {
		if err != redis.ErrKeyNotFound {
			// todo redis错误处理
		}
		ctx.Next()
	}
	// 返回数据
	result := map[string]interface{}{}
	err = jsoniter.UnmarshalFromString(resp, &result)
	if err != nil {
		// todo json错误
		ctx.Next()
	}
	_, _ = ctx.JSON(result)
	return
}
