package main

import (
	"fmt"
	pbproto "github.com/golang/protobuf/proto"
	"github.com/tigcode/containerfs/logger"
	ns "github.com/tigcode/containerfs/metanode/namespace"
	"github.com/tigcode/containerfs/metanode/raftopt"
	dp "github.com/tigcode/containerfs/proto/dp"
	mp "github.com/tigcode/containerfs/proto/mp"
	"github.com/tigcode/containerfs/utils"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"math/rand"
	"strconv"
	"time"
)

// for Datanode registry
func (s *MetaNodeServer) DatanodeRegistry(ctx context.Context, in *mp.Datanode) (*mp.DatanodeRegistryAck, error) {
	ack := mp.DatanodeRegistryAck{}
	ret, nameSpace := ns.GetNameSpace("Cluster")
	if ret != 0 {
		logger.Error("Get Cluster MetaNode NameSpace err for DatanodeRegistry")
		ack.Ret = ret
		return &ack, nil
	}
	ack.Ret = nameSpace.DatanodeRegistry(in)
	return &ack, nil
}

func (s *MetaNodeServer) GetAllDatanode(ctx context.Context, in *mp.GetAllDatanodeReq) (*mp.GetAllDatanodeAck, error) {
	ack := mp.GetAllDatanodeAck{}
	ret, nameSpace := ns.GetNameSpace("Cluster")
	if ret != 0 {
		logger.Error("Get Cluster MetaNode NameSpace err for GetAllDatanode")
		ack.Ret = ret
		return &ack, nil
	}

	v, err := nameSpace.GetAllDatanode()
	if err != nil {
		logger.Error("GetAllDatanode Info failed:%v", err)
		return &ack, err
	}

	ack.Datanodes = v
	ack.Ret = 0
	return &ack, nil
}

func (s *MetaNodeServer) DelDatanode(ctx context.Context, in *mp.DelDatanodeReq) (*mp.DelDatanodeAck, error) {
	ack := mp.DelDatanodeAck{}
	ret, nameSpace := ns.GetNameSpace("Cluster")
	if ret != 0 {
		logger.Error("Get Cluster MetaNode NameSpace err for DelDatanode")
		ack.Ret = ret
		return &ack, nil
	}

	port, _ := strconv.Atoi(in.Port)
	err := nameSpace.DataNodeDel(in.Ip, port)
	if err != nil {
		logger.Error("Delete DataNode(%v:%v) failed, err:%v", in.Ip, port, err)
		return &ack, err
	}
	ack.Ret = 0
	return &ack, nil
}

func generateRandomNumber(start int, end int, count int) []int {
	if end < start || (end-start) < count {
		return nil
	}

	nums := make([]int, 0)
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	for len(nums) < count {
		num := r.Intn((end - start)) + start

		exist := false
		for _, v := range nums {
			if v == num {
				exist = true
				break
			}
		}

		if !exist {
			nums = append(nums, num)
		}
	}

	return nums
}

func (s *MetaNodeServer) CreateVol(ctx context.Context, in *mp.CreateVolReq) (*mp.CreateVolAck, error) {

	s.Lock()
	defer s.Unlock()

	ack := mp.CreateVolAck{}
	voluuid, err := utils.GenUUID()
	if err != nil {
		logger.Error("Create volume uuid err:%v", err)
		return &ack, err
	}

	//the volume need block group total numbers
	var blkgrpnum int32
	if in.SpaceQuota%BlkSizeG == 0 {
		blkgrpnum = in.SpaceQuota / BlkSizeG
	} else {
		blkgrpnum = in.SpaceQuota/BlkSizeG + 1
		in.SpaceQuota = blkgrpnum * BlkSizeG
	}
	if blkgrpnum > 6 {
		blkgrpnum = 6
	}

	ret, nameSpace := ns.GetNameSpace("Cluster")
	if ret != 0 {
		logger.Error("Get Cluster NameSpace for CreateVol failed, ret:%v", ret)
		ack.Ret = ret
		return &ack, nil
	}

	rgID, err := nameSpace.AllocateRGID()
	if err != nil {
		logger.Error("Create Volume name:%v uuid:%v raftGroupID error:%v", voluuid, in.VolName, err)
		return &ack, err
	}

	vv, err := nameSpace.GetAllDatanode()
	if err != nil {
		logger.Error("GetAllDatanode Info failed:%v for CreateVol", err)
		return &ack, err
	}

	inuseNodes := make(map[string][]*mp.Datanode)
	allip := make([]string, 0)

	// pass bad status and free not enough datanodes
	for _, v := range vv {
		if v.Status != 0 || v.Free < 30 || v.Tier != in.Tier {
			continue
		}
		k := v.Ip
		inuseNodes[k] = append(inuseNodes[k], v)
	}

	for k, _ := range inuseNodes {
		allip = append(allip, k)
	}

	if len(allip) < 3 {
		logger.Error("Create Volume:%v Tier:%v but datanode nums:%v less than 3, so forbid CreateVol", voluuid, in.Tier, len(allip))
		ack.Ret = -1
		return &ack, nil
	}

	for i := int32(0); i < blkgrpnum; i++ {
		bgID, err := nameSpace.AllocateBGID()
		if err != nil {
			logger.Error("AllocateBGID for CreateVol failed, err:%v", err)
			return &ack, err
		}
		bg := &mp.BGP{Blocks: make([]*mp.Block, 0)}
		idxs := generateRandomNumber(0, len(allip), 3)
		if len(idxs) != 3 {
			ack.Ret = -1
			return &ack, nil
		}

		for n := 0; n < 3; n++ {
			block := mp.Block{}
			ipkey := allip[idxs[n]]
			idx := generateRandomNumber(0, len(inuseNodes[ipkey]), 1)
			if len(idx) <= 0 {
				ack.Ret = -1
				return &ack, nil
			}

			blockID, err := nameSpace.AllocateBlockID()
			if err != nil {
				logger.Error("AllocateBlockID for CreateVol failed, err:%v", err)
				return &ack, err
			}
			block.BlkID = blockID
			block.Ip = inuseNodes[ipkey][idx[0]].Ip
			block.Port = inuseNodes[ipkey][idx[0]].Port
			block.Path = inuseNodes[ipkey][idx[0]].MountPoint
			block.Status = inuseNodes[ipkey][idx[0]].Status
			block.BGID = bgID
			block.VolID = voluuid
			k := block.Ip + fmt.Sprintf(":%d", block.Port) + fmt.Sprintf("-%d", block.BlkID)
			v, _ := pbproto.Marshal(&block)
			err = nameSpace.RaftGroup.BlockSet(1, k, v)
			if err != nil {
				logger.Error("Create Volume:%v allocated blockgroup:%v block:%v failed:%v", voluuid, bgID, blockID, err)
				return &ack, err
			}
			bg.Blocks = append(bg.Blocks, &block)
			//update this datanode freesize
			inuseNodes[ipkey][idx[0]].Free = inuseNodes[ipkey][idx[0]].Free - 5
			key := ipkey + fmt.Sprintf(":%d", block.Port)
			val, _ := pbproto.Marshal(inuseNodes[ipkey][idx[0]])
			nameSpace.RaftGroup.DataNodeSet(1, key, val)

		}
		key := voluuid + fmt.Sprintf("-%d", bgID)
		val, _ := pbproto.Marshal(bg)
		err = nameSpace.RaftGroup.BGPSet(1, key, val)
		if err != nil {
			logger.Error("Create Volume:%v Set blockgroup:%v blocks:%v failed:%v", voluuid, bgID, bg, err)
			return &ack, err
		}
		logger.Debug("Create Volume:%v Tier:%v Set one blockgroup:%v blocks:%v to Cluster Map success", voluuid, in.Tier, bgID, bg)
	}

	vol := &mp.Volume{
		UUID:          voluuid,
		Name:          in.VolName,
		Tier:          in.Tier,
		TotalSize:     in.SpaceQuota,
		AllocatedSize: blkgrpnum * 5,
		RGID:          rgID,
	}

	val, _ := pbproto.Marshal(vol)
	nameSpace.RaftGroup.VOLSet(1, voluuid, val)
	ack.Ret = 0
	ack.UUID = voluuid
	ack.RaftGroupID = rgID

	retv := ns.CreateNameSpace(s.RaftServer, MetaNodeServerAddr.peers, MetaNodeServerAddr.nodeID, MetaNodeServerAddr.waldir, voluuid, rgID, false)
	if retv != 0 {
		logger.Error("CreateNameSpace local metanode failed for CreateVol, ret:%v", retv)
		ack.Ret = -1
		return &ack, nil
	}
	// send to follower metadatas to create
	for _, addr := range raftopt.AddrDatabase {

		logger.Debug("CreateNameSpace peer addr.Grpc %v s.Addr.Grpc %v", addr.Grpc, s.Addr.Grpc)

		if addr.Grpc == s.Addr.Grpc {
			continue
		}

		conn2, err2 := grpc.Dial(addr.Grpc, grpc.WithInsecure())
		if err2 != nil {
			logger.Error("told peers to  create NameSpace Failed ,err %v", err2)
			return &ack, err2
		}
		defer conn2.Close()
		mc := mp.NewMetaNodeClient(conn2)
		pmCreateNameSpaceReq := &mp.CreateNameSpaceReq{
			VolID:       voluuid,
			RaftGroupID: rgID,
			Type:        1,
		}
		pmCreateNameSpaceAck, err3 := mc.CreateNameSpace(context.Background(), pmCreateNameSpaceReq)
		if err3 != nil {
			logger.Error("told peers to  create NameSpace Failed ,err %v", err3)
			return &ack, err3
		}
		if pmCreateNameSpaceAck.Ret != 0 {
			logger.Error("told peers to create NameSpace Failed ...")
			ack.Ret = -1
			return &ack, nil
		}
	}

	return &ack, nil
}

func (s *MetaNodeServer) ExpandVolTS(ctx context.Context, in *mp.ExpandVolTSReq) (*mp.ExpandVolTSAck, error) {
	ack := mp.ExpandVolTSAck{}
	ret, nameSpace := ns.GetNameSpace("Cluster")
	if ret != 0 {
		logger.Error("Get Cluster NameSpace for ExpandVolTS failed, ret:%v", ret)
		ack.Ret = ret
		return &ack, nil
	}

	v, err := nameSpace.RaftGroup.VOLGet(1, in.VolID)
	if err != nil {
		logger.Error("Get volume info for ExpandVolTS failed, err:%v", err)
		return &ack, err
	}
	volume := mp.Volume{}
	err = pbproto.Unmarshal(v, &volume)
	if err != nil {
		return &ack, err
	}

	if in.ExpandQuota%BlkSizeG != 0 {
		blkgrpnum := in.ExpandQuota/BlkSizeG + 1
		in.ExpandQuota = blkgrpnum * BlkSizeG
	}

	volume.TotalSize = volume.TotalSize + in.ExpandQuota

	val, _ := pbproto.Marshal(&volume)
	err = nameSpace.RaftGroup.VOLSet(1, in.VolID, val)
	if err != nil {
		logger.Error("Update volume info for ExpandVolTS failed, err:%v", err)
		return &ack, err
	}
	ack.Ret = 0
	return &ack, nil
}

func (s *MetaNodeServer) ExpandVolRS(ctx context.Context, in *mp.ExpandVolRSReq) (*mp.ExpandVolRSAck, error) {

	s.Lock()
	defer s.Unlock()

	ack := mp.ExpandVolRSAck{}
	ret, cnameSpace := ns.GetNameSpace("Cluster")
	if ret != 0 {
		logger.Error("Get Cluster NameSpace for ExpandVolRS failed, ret:%v", ret)
		ack.Ret = ret
		return &ack, nil
	}

	v, err := cnameSpace.RaftGroup.VOLGet(1, in.VolID)
	if err != nil {
		logger.Error("Get volume info for ExpandVolRS failed, err:%v", err)
		return &ack, err
	}
	volume := mp.Volume{}
	err = pbproto.Unmarshal(v, &volume)
	if err != nil {
		return &ack, err
	}
	needExpandSize := volume.TotalSize - volume.AllocatedSize
	if needExpandSize <= 0 {
		ack.Ret = 0
		return &ack, nil
	}
	tier := volume.Tier

	var blkgrpnum int32
	if needExpandSize%BlkSizeG == 0 {
		blkgrpnum = needExpandSize / BlkSizeG
	} else {
		blkgrpnum = needExpandSize/BlkSizeG + 1
		needExpandSize = blkgrpnum * BlkSizeG
	}
	if blkgrpnum > 6 {
		blkgrpnum = 6
	}

	vv, err := cnameSpace.GetAllDatanode()
	if err != nil {
		logger.Error("GetAllDatanode Info failed:%v for ExpandVolRS", err)
		return &ack, err
	}

	inuseNodes := make(map[string][]*mp.Datanode)
	allip := make([]string, 0)

	// pass bad status and free not enough datanodes
	for _, v := range vv {
		if v.Status != 0 || v.Free < 30 || v.Tier != tier {
			continue
		}
		k := v.Ip
		inuseNodes[k] = append(inuseNodes[k], v)
	}

	for k, _ := range inuseNodes {
		allip = append(allip, k)
	}

	if len(allip) < 3 {
		logger.Error("Expand Volume:%v Tier:%v but datanode nums:%v less than 3, so forbid ExpandVol", in.VolID, tier, len(allip))
		ack.Ret = -1
		return &ack, nil
	}

	bgps := []*mp.BGP{}

	for i := int32(0); i < blkgrpnum; i++ {
		bgID, err := cnameSpace.AllocateBGID()
		if err != nil {
			logger.Error("AllocateBGID for ExpandVolRS failed, err:%v", err)
			return &ack, err
		}
		bg := &mp.BGP{Blocks: make([]*mp.Block, 0)}
		idxs := generateRandomNumber(0, len(allip), 3)
		if len(idxs) != 3 {
			ack.Ret = -1
			return &ack, nil
		}

		for n := 0; n < 3; n++ {
			block := mp.Block{}
			ipkey := allip[idxs[n]]
			idx := generateRandomNumber(0, len(inuseNodes[ipkey]), 1)
			if len(idx) <= 0 {
				ack.Ret = -1
				return &ack, nil
			}
			blockID, err := cnameSpace.AllocateBlockID()
			if err != nil {
				logger.Error("AllocateBlockID for ExpandVolRS failed, err:%v", err)
				return &ack, err
			}
			block.BlkID = blockID
			block.Ip = inuseNodes[ipkey][idx[0]].Ip
			block.Port = inuseNodes[ipkey][idx[0]].Port
			block.Path = inuseNodes[ipkey][idx[0]].MountPoint
			block.Status = inuseNodes[ipkey][idx[0]].Status
			block.BGID = bgID
			block.VolID = in.VolID
			k := block.Ip + fmt.Sprintf(":%d", block.Port) + fmt.Sprintf("-%d", block.BlkID)
			v, _ := pbproto.Marshal(&block)
			err = cnameSpace.RaftGroup.BlockSet(1, k, v)
			if err != nil {
				logger.Error("Expand Volume:%v allocated blockgroup:%v block:%v failed:%v for ExpandVolRS", in.VolID, bgID, blockID, err)
				return &ack, err
			}
			bg.Blocks = append(bg.Blocks, &block)
			//update this datanode freesize
			inuseNodes[ipkey][idx[0]].Free = inuseNodes[ipkey][idx[0]].Free - 5
			key := ipkey + fmt.Sprintf(":%d", block.Port)
			val, _ := pbproto.Marshal(inuseNodes[ipkey][idx[0]])
			cnameSpace.RaftGroup.DataNodeSet(1, key, val)

		}
		key := in.VolID + fmt.Sprintf("-%d", bgID)
		val, _ := pbproto.Marshal(bg)
		err = cnameSpace.RaftGroup.BGPSet(1, key, val)
		if err != nil {
			logger.Error("Expand Volume:%v Set blockgroup:%v blocks:%v failed:%v", in.VolID, bgID, bg, err)
			return &ack, err
		}
		bgps = append(bgps, bg)
		logger.Debug("Expand Volume:%v Set one blockgroup:%v blocks:%v to Cluster Map success", in.VolID, bgID, bg)
	}

	volume.AllocatedSize = volume.AllocatedSize + blkgrpnum*5

	val, _ := pbproto.Marshal(&volume)
	cnameSpace.RaftGroup.VOLSet(1, in.VolID, val)

	ack.Ret = 1
	ack.BGPS = bgps
	return &ack, nil
}

func (s *MetaNodeServer) DelVolRSForExpand(ctx context.Context, in *mp.DelVolRSForExpandReq) (*mp.DelVolRSForExpandAck, error) {
	ack := mp.DelVolRSForExpandAck{}
	ret, nameSpace := ns.GetNameSpace("Cluster")
	if ret != 0 {
		logger.Error("Get Cluster NameSpace for DelVolRSForExpand failed, ret:%v", ret)
		ack.Ret = ret
		return &ack, nil
	}

	delBGBadNum := 0
	for _, v := range in.BGPS {
		delBlkBadNum := 0
		for _, vv := range v.Blocks {
			blkKey := vv.Ip + fmt.Sprintf(":%d-%d", vv.Port, vv.BlkID)
			err := nameSpace.RaftGroup.BlockDel(1, blkKey)
			if err != nil {
				delBlkBadNum += 1
				continue
			}

			tmpDatanode := &mp.Datanode{}
			datanodeKey := vv.Ip + fmt.Sprintf(":%d", vv.Port)
			v, err := nameSpace.RaftGroup.DatanodeGet(1, datanodeKey)
			if err != nil {
				delBlkBadNum += 1
				continue
			}
			err = pbproto.Unmarshal(v, tmpDatanode)
			if err != nil {
				delBlkBadNum += 1
				continue
			}

			tmpDatanode.Free = tmpDatanode.Free + 5
			val, err := pbproto.Marshal(tmpDatanode)
			if err != nil {
				delBlkBadNum += 1
				continue
			}
			err = nameSpace.RaftGroup.DataNodeSet(1, datanodeKey, val)
			if err != nil {
				delBlkBadNum += 1
				continue
			}
		}
		bgKey := in.UUID + fmt.Sprintf("-%d", v.Blocks[0].BGID)
		err := nameSpace.RaftGroup.BGPDel(1, bgKey)
		if err != nil || delBlkBadNum != 0 {
			delBGBadNum += 1
		}
	}

	if delBGBadNum != 0 {
		ack.Ret = -1
		return &ack, nil
	}
	logger.Debug("Delete Volume:%v BlockGroups:%v Cluster Metadata Success for DelVolRSForExpand", in.UUID, in.BGPS)
	return &ack, nil
}

func (s *MetaNodeServer) DeleteVol(ctx context.Context, in *mp.DeleteVolReq) (*mp.DeleteVolAck, error) {
	ack := mp.DeleteVolAck{}
	ret, nameSpace := ns.GetNameSpace("Cluster")
	if ret != 0 {
		logger.Error("Get Cluster NameSpace for DeleteVol failed, ret:%v", ret)
		ack.Ret = ret
		return &ack, nil
	}

	err := nameSpace.RaftGroup.VOLDel(1, in.UUID)
	if err != nil {
		logger.Error("Delete Volume:%v map from Cluster MetaNodeAddr failed, err:%v", in.UUID, err)
		return &ack, err
	}

	value, err := nameSpace.RaftGroup.BGPGetRange(1, in.UUID)
	if err != nil {
		logger.Error("Get BGPS map from Cluster MetaNodeAddr for DeleteVol:%v failed, err:%v", in.UUID, err)
		return &ack, err
	}

	delBGBadNum := 0

	for _, v := range value {
		bgp := &mp.BGP{Blocks: make([]*mp.Block, 0)}

		err := pbproto.Unmarshal(v.V, bgp)
		if err != nil {
			continue
		}

		delBlkBadNum := 0
		for _, vv := range bgp.Blocks {
			blkKey := vv.Ip + fmt.Sprintf(":%d-%d", vv.Port, vv.BlkID)
			err := nameSpace.RaftGroup.BlockDel(1, blkKey)
			if err != nil {
				logger.Error("Delete Block:%v map from Cluster MetaNodeAddr for DeleteVol failed, err:%v", blkKey, err)
				delBlkBadNum += 1
				continue
			}

			tmpDatanode := &mp.Datanode{}
			datanodeKey := vv.Ip + fmt.Sprintf(":%d", vv.Port)
			v, err := nameSpace.RaftGroup.DatanodeGet(1, datanodeKey)
			if err != nil {
				logger.Error("Get DataNode:%v map from Cluster MetaNodeAddr for Update Free+5:%v failed for DeleteVol, err:%v", datanodeKey, err)
				delBlkBadNum += 1
				continue
			}
			err = pbproto.Unmarshal(v, tmpDatanode)
			if err != nil {
				delBlkBadNum += 1
				continue
			}

			tmpDatanode.Free = tmpDatanode.Free + 5
			val, err := pbproto.Marshal(tmpDatanode)
			if err != nil {
				delBlkBadNum += 1
				continue
			}
			err = nameSpace.RaftGroup.DataNodeSet(1, datanodeKey, val)
			if err != nil {
				logger.Error("Set DataNode:%v map value.Free Add 5G from Cluster MetaNodeAddr for failed for DeleteVol, err:%v", datanodeKey, err)
				delBlkBadNum += 1
				continue
			}
		}

		bgKey := in.UUID + fmt.Sprintf("-%d", bgp.Blocks[0].BGID)
		err = nameSpace.RaftGroup.BGPDel(1, bgKey)
		if err != nil || delBlkBadNum != 0 {
			logger.Error("Delete BG:%v from Cluster MetaNodeAddr failed", bgKey)
			delBGBadNum += 1
		}
	}
	if delBGBadNum != 0 {
		ack.Ret = -1
		return &ack, nil
	}
	logger.Debug("Delete Volume:%v from Cluster MetadataAddr Success", in.UUID)
	return &ack, nil
}

func (s *MetaNodeServer) Migrate(ctx context.Context, in *mp.MigrateReq) (*mp.MigrateAck, error) {

	ack := mp.MigrateAck{}

	go func() {
		dnIP := in.DataNodeIP
		dnPort := in.DataNodePort
		ret, nameSpace := ns.GetNameSpace("Cluster")
		if ret != 0 {
			logger.Error("Get Cluster NameSpace for Migrate failed, ret:%v", ret)
			ack.Ret = ret
			return
		}

		minKey := dnIP + fmt.Sprintf(":%d", dnPort)

		v, err := nameSpace.RaftGroup.DatanodeGet(1, minKey)
		if err != nil {
			logger.Error("Get Datanode Tier info for ExpandVolRS failed, err:%v", err)
			return
		}
		datanode := mp.Datanode{}
		err = pbproto.Unmarshal(v, &datanode)
		if err != nil {
			return
		}
		tier := datanode.Tier

		result, err := nameSpace.RaftGroup.BlockGetRange(1, minKey)
		if err != nil {
			logger.Error("Get Datanode %v Need Migrate Blocks failed, err:%v", minKey, err)
			return
		}
		totalNum := len(result)

		var successNum int
		var failedNum int

		logger.Debug("Migrating DataNode(%v:%v) Blocks Start ---------->>>>>>>>>>>>>>> Total nums:%v", dnIP, dnPort, totalNum)

		for i, v := range result {
			tBlk := &mp.Block{}
			err := pbproto.Unmarshal(v.V, tBlk)
			if err != nil {
				continue
			}

			ret := s.BeginMigrate(tBlk, tier)
			if ret != 0 {
				failedNum++
				logger.Error("Migrating DataNode(%v:%v) Block:%v failed ----->>>>>  Total num:%v , cur index:%v", dnIP, dnPort, tBlk, totalNum, i)
			} else {
				successNum++
				logger.Debug("Migrating DataNode(%v:%v) Block:%v success ----->>>>>  Total num:%v , cur index:%v", dnIP, dnPort, tBlk, totalNum, i)
			}
		}

		logger.Debug("Migrating DataNode(%v:%v) Blocks Done ----------<<<<<<<<<<<<<<<<< Total num:%v , Success num:%v , Failed num:%v", dnIP, dnPort, totalNum, successNum, failedNum)

	}()

	ack.Ret = 0
	return &ack, nil
}

func (s *MetaNodeServer) BeginMigrate(in *mp.Block, tier string) int {

	s.Lock()
	defer s.Unlock()

	ret, nameSpace := ns.GetNameSpace("Cluster")
	if ret != 0 {
		logger.Error("Get Cluster NameSpace for BeginMigrate failed, ret:%v", ret)
		return -1
	}

	bgKey := in.VolID + fmt.Sprintf("-%d", in.BGID)
	v, err := nameSpace.RaftGroup.BGPGet(1, bgKey)
	if err != nil {
		return -1
	}

	tbg := &mp.BGP{}
	err = pbproto.Unmarshal(v, tbg)
	if err != nil {
		return -1
	}

	blks := make([]*mp.Block, 0)
	for _, vv := range tbg.Blocks {
		if vv.BlkID == in.BlkID {
			continue
		}
		blks = append(blks, vv)
	}

	if len(blks) != 2 {
		logger.Error("Need Migrate Block:%v but the Backup BlockNum:%v not equal 2, so stop this Block Migrate", in, len(blks))
		return -1
	}

	datanodes, err := nameSpace.GetAllDatanode()
	if err != nil {
		logger.Error("GetAllDatanode Info failed:%v for Migrate Block", err)
		return -1
	}

	inuseNodes := make(map[string][]*mp.Datanode)
	allip := make([]string, 0)

	// pass bad status and free not enough datanodes
	for _, v := range datanodes {
		if v.Status != 0 || v.Free < 10 || v.Tier != tier || v.Ip == blks[0].Ip {
			continue
		}
		k := v.Ip
		inuseNodes[k] = append(inuseNodes[k], v)
	}

	for k, _ := range inuseNodes {
		allip = append(allip, k)
	}

	if len(allip) < 1 {
		logger.Error("Migrate block to New datanode but datanode nums:%v less than 1, so forbid Migrate", len(allip))
		return -1
	}

	idxs := generateRandomNumber(0, len(allip), 1)
	if len(idxs) != 1 {
		return -1
	}

	ipkey := allip[idxs[0]]
	idx := generateRandomNumber(0, len(inuseNodes[ipkey]), 1)
	if len(idx) <= 0 {
		return -1
	}

	newBlk := mp.Block{}
	blockID, err := nameSpace.AllocateBlockID()
	newBlk.BlkID = blockID
	newBlk.Ip = inuseNodes[ipkey][idx[0]].Ip
	newBlk.Port = inuseNodes[ipkey][idx[0]].Port
	newBlk.Path = inuseNodes[ipkey][idx[0]].MountPoint
	newBlk.Status = inuseNodes[ipkey][idx[0]].Status
	newBlk.BGID = in.BGID
	newBlk.VolID = in.VolID
	k := newBlk.Ip + fmt.Sprintf(":%d", newBlk.Port) + fmt.Sprintf("-%d", newBlk.BlkID)
	val, _ := pbproto.Marshal(&newBlk)
	err = nameSpace.RaftGroup.BlockSet(1, k, val)
	if err != nil {
		return -1
	}

	//update this datanode freesize
	inuseNodes[ipkey][idx[0]].Free = inuseNodes[ipkey][idx[0]].Free - 5
	key := ipkey + fmt.Sprintf(":%d", newBlk.Port)
	val, _ = pbproto.Marshal(inuseNodes[ipkey][idx[0]])
	nameSpace.RaftGroup.DataNodeSet(1, key, val)

	var okflag int
	for _, v := range blks {
		if v.Status != 0 {
			continue
		}
		logger.Debug("Migrate Block:%v copydata from BackBlock:%v to NewBlock:%v", in, v, newBlk)
		ret := beginMigrateBlk(v.BlkID, v.Ip, v.Port, v.Path, newBlk.BlkID, newBlk.Ip, newBlk.Port, newBlk.Path)
		if ret == 0 {
			blks = append(blks, &newBlk)
			pbg := &mp.BGP{}
			pbg.Blocks = blks
			val, _ := pbproto.Marshal(pbg)
			err1 := nameSpace.RaftGroup.BGPSet(1, bgKey, val)
			oldblkKey := in.Ip + fmt.Sprintf(":%d", in.Port) + fmt.Sprintf("-%d", in.BlkID)
			err2 := nameSpace.RaftGroup.BlockDel(1, oldblkKey)
			if err1 == nil && err2 == nil {
				logger.Debug("Migrate OldBlock:%v to NewBlock:%v Copydata from BackBlock:%v Success -- migrateUpdateMeta Success -- migrateUpdateDb Success!", in, newBlk, v)
				okflag = 1
			} else {
				logger.Debug("Migrate OldBlock:%v to NewBlock:%v Copydata from BackBlock:%v Success --  migrateUpdateMeta Success -- migrateUpdateDb Failed!", in, newBlk, v)
			}
			break

		} else {
			logger.Error("Migrate OldBlk:%v to NewBlk:%v Copydata from BackupBlk:%v Failed", in, newBlk, v)
			break
		}
	}
	if okflag == 1 {
		return 0
	} else {
		return -1
	}
}

func beginMigrateBlk(sid uint64, sip string, sport int32, smount string, did uint64, dip string, dport int32, dmount string) int32 {
	sDnAddr := sip + fmt.Sprintf(":%d", sport)
	conn, err := grpc.Dial(sDnAddr, grpc.WithInsecure())

	if err != nil {
		logger.Error("Migrate failed : Dial to DestDataNode:%v failed:%v !", sDnAddr, err)
		return -1
	}
	defer conn.Close()
	dc := dp.NewDataNodeClient(conn)
	tRecvMigrateReq := &dp.RecvMigrateReq{
		SrcBlkID: sid,
		SrcMount: smount,
		DstIP:    dip,
		DstPort:  dport,
		DstBlkID: did,
		DstMount: dmount,
	}

	ctx, _ := context.WithTimeout(context.Background(), 5*time.Minute)
	tRecvMigrateAck, err := dc.RecvMigrateMsg(ctx, tRecvMigrateReq)
	if err != nil {
		logger.Error("Migrate failed : DestDataNode:%v exec RecvMigrate function failed:%v !", sDnAddr, err)
		return -1
	}

	return tRecvMigrateAck.Ret
}

func DetectDataNodes(metaServer *MetaNodeServer) {
	ret, nameSpace := ns.GetNameSpace("Cluster")
	if ret != 0 {
		logger.Error("Get Cluster MetaNode NameSpace err for detectDataNodes")
		return
	}

	vv, err := nameSpace.GetAllDatanode()
	if err != nil {
		logger.Error("GetAllDatanode Info failed:%v for detectDatanodes", err)
		return
	}

	for _, v := range vv {
		go DetectDatanode(metaServer, v)
	}
}

func DetectDatanode(metaServer *MetaNodeServer, v *mp.Datanode) {
	dnAddr := v.Ip + fmt.Sprintf(":%d", v.Port)
	conn, err := grpc.Dial(dnAddr, grpc.WithInsecure())
	if err != nil {
		if v.Status == 0 {
			logger.Error("Detect DataNode:%v failed : Dial to datanode failed !", dnAddr)
			v.Status = 1
			SetDatanodeMap(v)
			logger.Debug("Detect Datanode(%v) status from good to bad, set datanode map success", dnAddr)
			//UpdateBlock(metaServer, v.Ip, v.Port, 1)
		}
		return
	}
	defer conn.Close()
	c := dp.NewDataNodeClient(conn)
	var DatanodeHealthCheckReq dp.DatanodeHealthCheckReq
	pDatanodeHealthCheckAck, err := c.DatanodeHealthCheck(context.Background(), &DatanodeHealthCheckReq)
	if err != nil {
		if v.Status == 0 {
			v.Status = 1
			SetDatanodeMap(v)
			logger.Debug("Detect Datanode(%v) status from good to bad, set datanode map success", dnAddr)
			//UpdateBlock(metaServer, v.Ip, v.Port, 1)
		}
		return
	}

	if pDatanodeHealthCheckAck.Status != 0 {
		if v.Status == 0 {
			v.Status = pDatanodeHealthCheckAck.Status
			v.Used = pDatanodeHealthCheckAck.Used
			SetDatanodeMap(v)
			logger.Debug("Detect Datanode(%v) status from good to bad, set datanode map success", dnAddr)
			//UpdateBlock(metaServer, v.Ip, v.Port, 1)
		}
		return
	}
	if v.Status != 0 {
		v.Status = 0
		logger.Debug("Detect Datanode(%v) status from bad to good, set datanode map success", dnAddr)
		//UpdateBlock(metaServer, v.Ip, v.Port, 0)
	}
	v.Used = pDatanodeHealthCheckAck.Used
	SetDatanodeMap(v)
	return
}

func SetDatanodeMap(v *mp.Datanode) int {
	ret, nameSpace := ns.GetNameSpace("Cluster")
	if ret != 0 {
		logger.Error("Get Cluster MetaNode NameSpace err for SetDatanodeMap")
		return -1
	}
	key := v.Ip + fmt.Sprintf(":%d", v.Port)
	val, _ := pbproto.Marshal(v)
	err := nameSpace.RaftGroup.DataNodeSet(1, key, val)
	if err != nil {
		logger.Error("Datanode set value:%v err:%v", val, err)
		return -1
	}
	return 0
}
