SELECT tid,type,hash,COALESCE(uploader,0) AS uploader FROM `{{prefix}}textures` ORDER BY tid
