package apiBuilder

import (
	"github.com/kataras/iris/v12"
	"github.com/kataras/iris/v12/core/router"
	"github.com/pkg/errors"
	"log"
	"reflect"
	"strconv"
	"strings"
	"time"
	"xorm.io/xorm"
)

var nowApi *Api

func isContain(items []string, item string) bool {
	for _, eachItem := range items {
		if eachItem == item {
			return true
		}
	}
	return false
}

type Config struct {
	App        *iris.Application
	StructList []interface{}
	Engine     *xorm.Engine
	Prefix     string // 访问前缀 例如:/api/v1
}

type modelInfo struct {
	MapName       string
	Model         interface{}
	Private       bool
	KeyName       string
	TableColName  string
	StructColName string
	FieldList     TableFieldsResp `json:"field_list"`
}

type Api struct {
	Config     *Config
	ModelLists []modelInfo
}

func New(c Config) *Api {
	nowApi = &Api{
		Config:     &c,
		ModelLists: []modelInfo{},
	}
	return nowApi
}

func (c *Api) Run() {
	p := c.Config.App.Party(c.Config.Prefix)
	for _, model := range c.Config.StructList {
		apiName := c.Config.Engine.TableName(model)
		var api iris.Party
		api = p.Party(apiName)

		// 私密访问
		enablePrivateAccess := false
		privateContextKey := ""
		privateColName := ""
		if processor, ok := model.(PrivateAccessProcess); ok {
			enablePrivateAccess = true
			privateContextKey = processor.ApiPrivateContextKey()
			privateColName = processor.ApiPrivateTableColName()
		}

		info := modelInfo{
			MapName:       apiName,
			Model:         model,
			Private:       enablePrivateAccess,
			KeyName:       privateContextKey,
			StructColName: privateColName,
			FieldList:     c.tableNameReflectFieldsAndTypes(apiName),
		}

		if enablePrivateAccess {
			for _, field := range info.FieldList.Fields {
				if field.Name == privateColName {
					info.TableColName = field.MapName
					break
				}
			}
		}

		c.ModelLists = append(c.ModelLists, info)

		// 判断使用拥有前置访问中间件
		if processor, ok := model.(GlobalPreMiddlewareProcess); ok {
			api.Use(processor.ApiGlobalPreMiddleware)
		}
		var disableMethods []string
		// 判断是否设置了禁用方法
		if processor, ok := model.(DisableMethodsProcess); ok {
			disableMethods = processor.ApiDisableMethods()
		}

		if !isContain(disableMethods, "get(all)") {
			var route *router.Route
			// 判断是否覆盖了方法
			if processor, ok := model.(GetAllProcess); ok {
				route = api.Handle("GET", "/", processor.ApiGetAll)
			} else {
				route = api.Handle("GET", "/", GetAllFunc)
			}
			if processor, ok := model.(GetAllPreMiddlewareProcess); ok {
				route.Use(processor.ApiGetAllPreMiddleware)
			}
		}

		if !isContain(disableMethods, "get(single)") {
			var route *router.Route
			// 判断是否覆盖了方法
			if processor, ok := model.(GetSingleProcess); ok {
				route = api.Handle("GET", "/{id:uint64}", processor.ApiGetSingle)
			} else {
				route = api.Handle("GET", "/{id:uint64}", GetSingle)
			}
			if processor, ok := model.(GetSinglePreMiddlewareProcess); ok {
				route.Use(processor.ApiGetSinglePreMiddleware)
			}
		}

		if !isContain(disableMethods, "post") {
			var route *router.Route
			// 判断是否覆盖了方法
			if processor, ok := model.(PostProcess); ok {
				route = api.Handle("POST", "/", processor.ApiPost)
			} else {
				route = api.Handle("POST", "/", AddData)
			}
			if processor, ok := model.(PostPreMiddlewareProcess); ok {
				route.Use(processor.ApiPostPreMiddleware)
			}
		}

		if !isContain(disableMethods, "put") {
			var route *router.Route
			// 判断是否覆盖了方法
			if processor, ok := model.(PutProcess); ok {
				route = api.Handle("PUT", "/{id:uint64}", processor.ApiPut)
			} else {
				route = api.Handle("PUT", "/{id:uint64}", EditData)
			}
			if processor, ok := model.(PutPreMiddlewareProcess); ok {
				route.Use(processor.ApiPutPreMiddleware)
			}
		}

		if !isContain(disableMethods, "delete") {
			var route *router.Route
			// 判断是否覆盖了方法
			if processor, ok := model.(DeleteProcess); ok {
				route = api.Handle("DELETE", "/{id:uint64}", processor.ApiDelete)
			} else {
				route = api.Handle("DELETE", "/{id:uint64}", DeleteData)
			}
			if processor, ok := model.(PutDeleteMiddlewareProcess); ok {
				route.Use(processor.ApiDeletePreMiddleware)
			}
		}

	}

}

func (c *Api) pathGetModel(pathName string) modelInfo {
	p := strings.Split(pathName, "/")[1]
	for _, m := range c.ModelLists {
		if m.MapName == p {
			return m
		}
	}
	return modelInfo{}
}

func (c *Api) tableNameReflectFieldsAndTypes(tableName string) TableFieldsResp {
	for _, item := range c.Config.StructList {
		if c.Config.Engine.TableName(item) == tableName {
			modelInfo, err := c.Config.Engine.TableInfo(item)
			if err != nil {
				return TableFieldsResp{}
			}
			var resp TableFieldsResp
			// 获取三要素
			values := c.tableNameGetNestedStructMaps(reflect.TypeOf(item))
			resp.Fields = values
			resp.AutoIncrement = modelInfo.AutoIncrement
			resp.Version = modelInfo.Version
			resp.Deleted = modelInfo.Deleted
			resp.Created = modelInfo.Created
			resp.Updated = modelInfo.Updated
			return resp
		}
	}
	return TableFieldsResp{}

}

// 通过模型名获取所有列信息 名称 类型 xorm tag validator comment
func (c *Api) tableNameGetNestedStructMaps(r reflect.Type) []structInfo {
	if r.Kind() == reflect.Ptr {
		r = r.Elem()
	}
	if r.Kind() != reflect.Struct {
		return nil
	}
	v := reflect.New(r).Elem()
	result := make([]structInfo, 0)
	for i := 0; i < r.NumField(); i++ {
		field := r.Field(i)
		v := reflect.Indirect(v).FieldByName(field.Name)
		fieldValue := v.Interface()
		var d structInfo

		switch fieldValue.(type) {
		case time.Time, time.Duration:
			d.Name = field.Name
			d.Types = field.Type.String()
			d.XormTags = field.Tag.Get("xorm")
			d.ValidateTags = field.Tag.Get("validate")
			d.CommentTags = field.Tag.Get("comment")
			d.AttrTags = field.Tag.Get("attr")
			d.MapName = c.Config.Engine.GetColumnMapper().Obj2Table(field.Name)
			result = append(result, d)
			continue
		}
		if field.Type.Kind() == reflect.Struct {
			values := c.tableNameGetNestedStructMaps(field.Type)
			result = append(result, values...)
			continue
		}
		d.Name = field.Name
		d.Types = field.Type.String()
		d.MapName = c.Config.Engine.GetColumnMapper().Obj2Table(field.Name)
		d.XormTags = field.Tag.Get("xorm")
		d.CommentTags = field.Tag.Get("comment")
		d.AttrTags = field.Tag.Get("attr")
		d.ValidateTags = field.Tag.Get("validate")
		result = append(result, d)
	}
	return result
}

// 通过模型名获取实例
func (c *Api) tableNameGetModel(tableName string) (interface{}, error) {
	for _, item := range c.ModelLists {
		if item.MapName == tableName {
			return item, nil
		}
	}
	return nil, errors.New("未找到模型")
}

// 通过模型名获取模型信息
func (c *Api) tableNameGetModelInfo(tableName string) (modelInfo, error) {
	for _, l := range c.ModelLists {
		if l.MapName == tableName {
			return l, nil
		}
	}
	return modelInfo{}, errors.New("未找到模型")
}

// 获取内容
func (c *Api) getValue(ctx iris.Context, k string) string {
	b := ctx.PostValueTrim(k)
	if len(b) < 1 {
		b = ctx.FormValue(k)
	}
	return b
}

// 对应关系获取
func (c *Api) getCtxValues(routerName string, ctx iris.Context) (reflect.Value, error) {
	// 先获取到字段信息
	cb, err := c.tableNameGetModelInfo(routerName)
	if err != nil {
		return reflect.Value{}, err
	}
	t := reflect.TypeOf(cb.Model)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	newInstance := reflect.New(t)

	for _, column := range cb.FieldList.Fields {
		if column.MapName != cb.FieldList.AutoIncrement {
			if column.MapName == cb.FieldList.Updated || column.MapName == cb.FieldList.Deleted {
				continue
			}
			if len(cb.FieldList.Created) >= 1 {
				var equal = false
				for k, _ := range cb.FieldList.Created {
					if column.MapName == k {
						equal = true
						break
					}
				}
				if equal {
					continue
				}
			}
			content := c.getValue(ctx, column.MapName)
			switch column.Types {
			case "string":
				newInstance.Elem().FieldByName(column.Name).SetString(content)
				continue
			case "int", "int8", "int16", "int32", "int64", "time.Duration":
				d, err := strconv.ParseInt(content, 10, 64)
				if err != nil {
					log.Printf("解析出int出错")
				}
				newInstance.Elem().FieldByName(column.Name).SetInt(d)
				continue
			case "uint", "uint8", "uint16", "uint32", "uint64":
				d, err := strconv.ParseUint(content, 10, 64)
				if err != nil {
					log.Println("解析出uint出错")
				}
				newInstance.Elem().FieldByName(column.Name).SetUint(d)
				continue
			case "float32", "float64":
				d, err := strconv.ParseFloat(content, 64)
				if err != nil {
					log.Println("解析出float出错")
				}
				newInstance.Elem().FieldByName(column.Name).SetFloat(d)
				continue
			case "bool":
				d, err := parseBool(content)
				if err != nil {
					log.Println("解析出bool出错")
				}
				newInstance.Elem().FieldByName(column.Name).SetBool(d)
				continue
			case "time", "time.Time":
				var tt reflect.Value
				// 判断是否是字符串
				if IsNum(content) {
					// 这里需要转换成时间
					d, err := strconv.ParseInt(content, 10, 64)
					if err != nil {
						return reflect.Value{}, errors.Wrap(err, "time change to int error")
					}
					tt = reflect.ValueOf(time.Unix(d, 0))
				} else {
					formatTime, err := time.ParseInLocation("2006-01-02 15:04:05", content, time.Local)
					if err != nil {
						return reflect.Value{}, errors.Wrap(err, "time parse location error")
					}
					tt = reflect.ValueOf(formatTime)
				}
				newInstance.Elem().FieldByName(column.Name).Set(tt)
				continue
			}
		}
	}

	return newInstance, nil
}

// 模型反射一个新
func (c *Api) newModel(routerName string) interface{} {
	cb, _ := c.tableNameGetModelInfo(routerName)
	t := reflect.TypeOf(cb.Model)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	newInstance := reflect.New(t)
	return newInstance.Interface()
}
