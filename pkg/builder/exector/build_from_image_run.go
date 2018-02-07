
// RAINBOND, Application Management Platform
// Copyright (C) 2014-2017 Goodrain Co., Ltd.
 
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. For any non-GPL usage of Rainbond,
// one or multiple Commercial Licenses authorized by Goodrain Co., Ltd.
// must be obtained first.
 
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU General Public License for more details.
 
// You should have received a copy of the GNU General Public License
// along with this program. If not, see <http://www.gnu.org/licenses/>.

package exector


import (
	"github.com/Sirupsen/logrus"
	"time"
	"fmt"
	"github.com/goodrain/rainbond/pkg/event"
	"github.com/tidwall/gjson"
	"github.com/docker/docker/api/types"
	//"github.com/docker/docker/client"
	"github.com/docker/engine-api/client"
	"github.com/goodrain/rainbond/pkg/builder/sources"
	"github.com/akkuman/parseConfig"
	"github.com/goodrain/rainbond/pkg/db"
	"github.com/goodrain/rainbond/pkg/worker/discover/model"
	"github.com/goodrain/rainbond/pkg/builder/apiHandler"
)

//ImageBuildItem ImageBuildItem
type ImageBuildItem struct {
	Namespace 		string `json:"namespace"`
	TenantName 		string `json:"tenant_name"`
	ServiceAlias 	string `json:"service_alias"`
	Image 			string `json:"image"`
	DestImage 		string `json:"dest_image"`
	Logger 			event.Logger `json:"logger"`
	EventID	 		string `json:"event_id"`
	DockerClient    *client.Client	
	Config          parseConfig.Config
	TenantID        string
	ServiceID 		string
	DeployVersion   string
}

//NewImageBuildItem 创建实体
func NewImageBuildItem(in []byte) *ImageBuildItem {
	eventID := gjson.GetBytes(in, "event_id").String()
	logger := event.GetManager().GetLogger(eventID)
	return &ImageBuildItem{
		Namespace: gjson.GetBytes(in, "namespace").String(),
		TenantName:  gjson.GetBytes(in, "tenant_name").String(),
		ServiceAlias: gjson.GetBytes(in, "service_alias").String(),
		Image: gjson.GetBytes(in, "image").String(),
		DeployVersion: gjson.GetBytes(in, "deploy_version").String(),
		Logger: logger,
		EventID: eventID,
		Config: GetBuilderConfig(),
	}
}

//Run Run
func (i *ImageBuildItem) Run(timeout time.Duration) error {
	_, err := sources.ImagePull(i.DockerClient, i.Image, types.ImagePullOptions{}, i.Logger, 3)
	if err != nil {
		logrus.Errorf("pull image %s error: %s", i.Image, err.Error())
		i.Logger.Error(fmt.Sprintf("拉取镜像: %s失败， %s", i.Image, err.Error()), map[string]string{"step": "builder-exector", "status":"failure"})
		return err
	}
	localImageURL := i.ImageNameHandler(i.Image)
	if err := sources.ImageTag(i.DockerClient, i.Image, localImageURL, i.Logger, 1); err != nil {
		logrus.Errorf("change image tag error: %s", err.Error())
		i.Logger.Error(fmt.Sprintf("修改镜像tag: %s -> %s 失败", i.Image, localImageURL), map[string]string{"step": "builder-exector", "status": "failure"})
		return err
	}
	err = sources.ImagePush(i.DockerClient, localImageURL, types.ImagePushOptions{}, i.Logger, 2)
	if err != nil {
		logrus.Errorf("push image into registry error: %s", err.Error())
		i.Logger.Error("推送镜像至镜像仓库失败", map[string]string{"step": "builder-exector", "status":"failure"})
		return err
	}
	if err := i.StorageLocalImageURL(localImageURL); err != nil {
		logrus.Errorf("storage image url error: %s", err.Error())
		i.Logger.Error("存储镜像信息失败", map[string]string{"step":"builder-exector", "status":"failure"})
		return err
	}
	if err := i.StorageVersionInfo(localImageURL); err != nil {
		logrus.Errorf("storage version info error, ignor it: %s", err.Error())
	}
	i.Logger.Info("应用同步完成，开始启动应用", map[string]string{"step": "build-exector"})
	if err := apiHandler.UpgradeService(i.TenantName, i.ServiceAlias, i.CreateUpgradeTaskBody()); err != nil {
		i.Logger.Error("启动应用失败，请手动启动", map[string]string{"step": "callback", "status": "failure"})
		logrus.Errorf("rolling update service error, %s", err.Error())
	}
	return nil
}

//ImageNameHandler 根据平台配置处理镜像名称
func (i *ImageBuildItem) ImageNameHandler(source string) string {
	currRegistry := i.Config.Get("publish > image > curr_registry").(string)
	allRegistry := i.Config.Get("publish > image > all_registry").(string)
	logrus.Debugf("curr all resigtry is %s, %s", currRegistry, allRegistry)
	imageModel := sources.ImageNameHandle(source)
	localImageURL := fmt.Sprintf("%s/%s:%s_%s", currRegistry, imageModel.Name, imageModel.Tag, i.ServiceAlias)
	return localImageURL
}

//StorageLocalImageURL 修改的镜像名称存库
func (i *ImageBuildItem) StorageLocalImageURL(imageURL string) error {
	tenant, err := db.GetManager().TenantDao().GetTenantIDByName(i.TenantName)
	if err != nil {
		return err
	}
	service, err := db.GetManager().TenantServiceDao().GetServiceByTenantIDAndServiceAlias(tenant.UUID, i.ServiceAlias)
	if err != nil {
		return err
	}
	service.ImageName = imageURL
	if err := db.GetManager().TenantServiceDao().UpdateModel(service); err != nil {
		return err
	}
	i.TenantID = tenant.UUID
	i.ServiceID = service.ServiceID
	return nil
}

//StorageVersionInfo 存储version信息
func (i *ImageBuildItem) StorageVersionInfo(imageURL string) error {
	version, err := db.GetManager().VersionInfoDao().GetVersionByEventID(i.EventID)
	if err != nil {
		return err
	}
	version.DeliveredType = "image"
	version.DeliveredPath = imageURL
	if err := db.GetManager().VersionInfoDao().UpdateModel(version); err != nil {
		return err
	}
	return  nil
}

//CreateUpgradeTaskBody 构造消息体
func (i *ImageBuildItem) CreateUpgradeTaskBody() *model.RollingUpgradeTaskBody{
	return &model.RollingUpgradeTaskBody{
		TenantID: i.TenantID,
		ServiceID: i.ServiceID,
		NewDeployVersion: i.DeployVersion,
		EventID: i.EventID,
	}
}

//UpdateVersionInfo 更新任务执行结果
func (i *ImageBuildItem) UpdateVersionInfo(status string) error {
	version,err :=db.GetManager().VersionInfoDao().GetVersionByEventID(i.EventID)
	if err != nil {
		return err
	}
	version.FinalStatus = status
	if err := db.GetManager().VersionInfoDao().UpdateModel(version); err != nil {
		return err
	}
	return nil
} 