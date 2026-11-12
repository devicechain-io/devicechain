---
title: 资产、客户与区域
sidebar_position: 9
---

import GuideShot from '@site/src/components/GuideShot';

# 资产、客户与区域 {#assets-customers-and-areas}

设备负责测量事物。注册表则用来描述这些事物及其组织方式：设备所监测的**资产**、你所服务的**客户**，以及设备群分布的**区域**。本章面向负责搭建和维护这套结构的租户用户和管理员。

这三类对象的使用方式相同，学会一类就等于学会了全部。每一类在侧边栏的 **Organization**（组织）下都有三个界面：

| 类别 | 实例 | 模板 | 集合 |
| --- | --- | --- | --- |
| 资产 | Assets | Asset Types | Asset Groups |
| 客户 | Customers | Customer Types | Customer Groups |
| 区域 | Areas | Area Types | Area Groups |

**类型**用于对实例分类，例如资产的“HVAC Unit”（暖通空调机组）或区域的“Building”（楼宇）。**分组**是同一类别实例的命名集合。Areas 菜单中还包含 Geofences（地理围栏），它另有专章：[地理围栏](/user-guide/geofences)。

这些实体与设备以及彼此之间的关系，请参阅[领域模型](/concepts/domain-model)。

## 列表 {#lists}

每个列表分页显示该类别的实例，表格下方有 **Prev**（上一页）和 **Next**（下一页）。资产、客户和区域的列分别是 Token、Name、Type 和 Created。点击某一行即可打开。

<GuideShot id="assets-list" alt="Assets 列表：五个资产，包括三台暖通空调机组及其两个部件，每个都有令牌、名称、HVAC Unit 类型和创建日期。右上角有“New asset”按钮。" />

**令牌**（token）是实例的标识符。其他界面、规则和 API 都通过令牌引用它，因此创建后不可更改。**名称**是供人阅读的标签，可以随时修改。

<GuideShot id="customers-list" alt="Customers 列表，只有一个客户 Acme Properties，类型为 Facility Owner。" />

<GuideShot id="areas-list" alt="Areas 列表，包含三栋楼宇，区域类型均为 Building。展开的 Areas 菜单显示 Areas、Area Types、Area Groups 和 Geofences。" />

## 创建实例 {#creating-an-instance}

点击 **New asset**（新建资产；客户和区域分别为 **New customer** 和 **New area**）。会弹出一个抽屉，所有类别都有相同的四个字段：

- **Asset type**（资产类型；客户、区域对应 customer type、area type）：该实例所属的模板。如果尚不存在任何类型，选择器会提示你先创建类型。
- **Token**（令牌）：标识符。旁边的按钮会按运维人员为此类实体设定的令牌模式生成令牌，该模式显示在字段下方。你也可以自己输入。令牌可以包含字母、数字、连字符和下划线，且必须以字母或数字开头。
- **Name**（名称）和 **Description**（描述）：自由文本。

<GuideShot id="assets-new" alt="New asset 抽屉，包含 Asset type 选择器、显示令牌模式 {slug} 并带有生成令牌按钮的 Token 字段、Name 和 Description 字段，以及“Create asset”按钮。" />

点击 **Create asset**（创建资产）保存。在选择类型并输入令牌之前，该按钮保持禁用。

## 详情页 {#the-detail-page}

打开某个实例即可看到它的详情页。页眉显示名称、令牌（带复制按钮）和类型。右上角是 **Delete**（删除）；它会要求确认，且删除无法撤销。如果仍有其他对象依赖该实例，控制台会显示服务器的拒绝原因。

删除客户还会停用它的令牌：在此租户中不能再用同一令牌创建另一个客户。告警、命令、更新分配、共享仪表板和事件历史都通过令牌指向客户，并在客户删除后保留，因此使用该令牌的新客户会继承它们。请为新客户选择其他令牌。

**Basic**（基本信息）选项卡的表单与创建实例时相同。你可以修改类型、名称和描述，然后点击 **Save changes**（保存更改）。令牌可见但不可编辑。

<GuideShot id="asset-detail-basic" alt="HVAC Unit 1 的详情页，位于 Basic 选项卡：Asset type 为 HVAC Unit，只读的 Token 并附说明资产 ID 不可更改，以及 Name、Description 和“Save changes”按钮。Basic 旁边是 Facets、Hierarchy、Properties 和 Devices 选项卡。" />

每个资产、客户和区域还有一个 **Facets**（分面）选项卡，见[下文](#facets)。资产另有三个选项卡：Hierarchy、Properties 和 Devices。客户另有两个：Devices 和 Users。

### 层次结构（资产） {#hierarchy-assets}

资产构成一棵树。水泵可以挂在它所属的机房之下，风机电机可以挂在它所属的暖通空调机组之下。**Hierarchy**（层次结构）选项卡显示该资产所处的位置及其直接下级。

- **Location in the hierarchy**（在层次结构中的位置）显示该资产上方的路径，或说明它位于顶层。
- 要移动资产，请选择一个 **Parent asset**（父资产）并点击 **Place under parent**（放到父资产之下）。这会替换它当前的父资产，它自己的子资产随之移动。
- **Detach**（分离）将资产从其父资产之下移出，使其成为顶层资产。
- **Directly below**（直接下级）列出该资产的子资产。点击其中一个即可打开。

<GuideShot id="asset-detail-hierarchy" alt="HVAC Unit 1 的 Hierarchy 选项卡。它说明该资产位于层次结构顶部，提供带有 Place under parent 和 Detach 按钮的 Parent asset 选择器，并列出其直接下级的两个资产：送风风机和压缩机。" />

### 属性（资产）{#properties-assets}

资产类型可以发布一份**属性契约**：其资产必须携带的一组具名、带类型的值，例如制造商或额定容量。资产的 **Properties**（属性）选项卡就是填写这些值的地方。如果资产的类型尚未发布契约，该选项卡会说明这一点，此时资产不携带任何属性。契约在资产类型上设置，参见[资产类型](#asset-types)。

### 设备（资产）{#devices-assets}

**Devices**（设备）选项卡列出分配给该资产的设备，这是设备分配关系的反向视图。点击令牌即可打开设备。要将设备分配给资产，请参阅[管理分配](/guides/managing-assignments)。

<GuideShot id="asset-detail-devices" alt="HVAC Unit 1 的 Devices 选项卡，只有一台设备 bp-therm-001。" />

### 设备与用户（客户） {#customer-devices-and-users}

客户的 **Devices**（设备）选项卡以与资产相同的方式列出分配给它的设备。这些就是该客户自己的用户能看到的设备。

**Users**（用户）选项卡列出访问权限仅限于该客户的成员，即该客户自己的登录账号。**Invite user**（邀请用户）邀请某人，并且其访问权限已限定于该客户。只有当你能读取租户的成员时，才会显示此选项卡。客户用户的介绍参见[成员、角色与客户用户](/user-guide/members-roles-and-customer-users#customer-users)。

{/* TODO(screenshot): customer-detail-users, a customer's Users tab. */}

### 分面 {#facets}

**Facets** 选项卡保存该实例的**分面值**：你在 Browse 界面中用来浏览和筛选的分类值，例如楼宇所在的气候区。分面值保存在实例本身上。Facets 界面只负责声明哪些键是分面，因此已声明但这里没有值的分面不会匹配任何内容。

如果该类别尚未声明任何分面，选项卡会说明这一点，并指引你前往 Facets 界面。下方的 **Other attributes**（其他属性）列出实例携带的其他所有属性。由设备上报的值（CLIENT）或由平台设置的值（SERVER）仅供参考，分面筛选不会读取它们。**Add attribute**（添加属性）会保存一个新键；若要按它浏览，请先在 Facets 界面中声明。

<GuideShot id="customer-detail-facets" alt="客户 Acme Properties 的 Facets 选项卡。在 Facet values 下，一条说明表示该类别尚未声明任何分面。在 Other attributes 下是“Add attribute”按钮和“No other attributes”说明。" />

<GuideShot id="area-detail-facets" alt="区域 Building 1 的 Facets 选项卡，布局相同：区域类别未声明分面，也没有其他属性。" />

编辑属性值需要 `device:write` 权限，对所有类别都是如此。分面、Browse 和动态分组见[分组、分面与浏览](/user-guide/groups-facets-browse)。

## 类型 {#types}

类型是对实例分类的模板。每个类型列表都有一个 Appearance 列（该类型使用的徽章）、令牌、描述和创建日期。

<GuideShot id="area-types-list" alt="Area Types 列表，只有一个类型 Building，令牌为 bp-building。右上角有“New area type”按钮。" />

类型的详情页有用于名称和描述的 **Basic** 选项卡，以及用于设置徽章图标和颜色的 **Appearance**（外观）选项卡。该类型的实例在任何位置显示时都会带上这枚徽章。

### 资产类型：属性契约与版本 {#asset-types}

资产类型还有两个选项卡，因为资产类型可以声明其资产所携带的内容。

**Properties** 选项卡保存草稿状态的 **Property contract**（属性契约）：一个属性描述符的 JSON 数组。每个描述符有 `name` 和 `dataType`（`DOUBLE`、`INT`、`BOOLEAN` 或 `STRING`），以及可选的 `description`、`unit`、`required`、`default`、`minValue`、`maxValue` 和 `enum`。该类型的资产随后会按它校验：未声明的属性、类型错误的值或缺少必需属性都会被拒绝。

需要了解两点：

- 将该字段留空会**撤回**契约。这与空列表 `[]` 不同，后者声明此类型的资产不携带任何属性。
- 保存只会更改草稿。在你再次发布之前，资产按**已发布**版本校验。

<GuideShot id="asset-type-properties" alt="HVAC Unit 资产类型的 Properties 选项卡。一条说明指出此类型的资产按已发布的版本 1 校验，修改须再次发布后才生效。Property contract 字段包含一个 JSON 数组，声明了 manufacturer、ratedCapacityKw、installedYear 和 serviceTier。" />

**Versions**（版本）选项卡显示当前生效的版本和每个已发布版本，包括其标签、发布时间和发布者。

- **Publish**（发布）将当前草稿冻结为一个新版本，并使其成为资产的校验依据。你可以为该版本添加标签和描述。
- **Roll back**（回滚）使较早的版本成为生效版本。草稿不会改变。已存储的属性既不会被重新校验，也不会被改写，因此不再符合要求的资产会保持原样，直到有人重新写入其属性。

发布、回滚和编辑属性契约同样需要 `device:write`。

<GuideShot id="asset-type-versions" alt="HVAC Unit 资产类型的 Versions 选项卡。Active version：1。只有一行：版本 1，标记为 active，标签为 Nameplate properties，附有发布时间和发布者，以及 Roll back 操作。右上角有 Publish 按钮。" />

## 分组 {#groups}

分组收集同一类别的实例：Asset Groups 包含资产，Customer Groups 包含客户，Area Groups 包含区域。分组的列表页和详情页与其他界面的用法一致。其详情页有一个 **Members**（成员）选项卡，用于向分组添加和移除实例。

分组也可以是**动态**的：它不是你手工维护的列表，而是一个已保存的筛选条件，每次读取时重新计算成员。你在 Browse 界面创建这类分组，参见[分组、分面与浏览](/user-guide/groups-facets-browse)。

与删除客户一样，删除分组会停用其令牌：在此租户中不能再用同一令牌创建另一个分组。成员的访问权限可以仅限于某个分组，设备位于分组期间写入的记录也通过令牌指向该分组，因此使用该令牌的新分组会继承这些记录。请为新分组换一个令牌。

## 相关内容 {#related}

- [领域模型](/concepts/domain-model)：资产、资产层次结构、分组和属性。
- [管理分配](/guides/managing-assignments)：将设备关联到资产、区域和客户。
- [分组、分面与浏览](/user-guide/groups-facets-browse)
- [地理围栏](/user-guide/geofences)
- [成员、角色与客户用户](/user-guide/members-roles-and-customer-users)
