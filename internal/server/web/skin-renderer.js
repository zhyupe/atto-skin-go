/* Minecraft cuboid UV layout; no external renderer or continuous render loop.
 * Layout reference: https://github.com/bs-community/skinview3d/blob/master/src/model.ts
 * Legacy skins mirror the right arm/leg onto the left; fully opaque PNGs omit overlays.
 */
(() => {
  'use strict';
  function parts(model, legacy = false) {
    const arm = model === 'slim' ? 3 : 4;
    return [
      {name:'head', x:-4,y:24,z:-4,w:8,h:8,d:8,uv:[0,0],overlay:[32,0],expand:.5},
      {name:'body', x:-4,y:12,z:-2,w:8,h:12,d:4,uv:[16,16],overlay:[16,32]},
      {name:'rightArm', x:-4-arm,y:12,z:-2,w:arm,h:12,d:4,uv:[40,16],overlay:[40,32]},
      {name:'leftArm', x:4,y:12,z:-2,w:arm,h:12,d:4,uv:legacy?[40,16]:[32,48],overlay:[48,48],mirror:legacy},
      {name:'rightLeg', x:-4,y:0,z:-2,w:4,h:12,d:4,uv:[0,16],overlay:[0,32]},
      {name:'leftLeg', x:0,y:0,z:-2,w:4,h:12,d:4,uv:legacy?[0,16]:[16,48],overlay:[0,48],mirror:legacy}
    ];
  }
  function faces(p, origin, expand = 0) {
    const [u,v] = origin, {w,h,d} = p;
    const x=p.x-expand,y=p.y-expand,z=p.z-expand;
    const sx=(w+expand*2)/w,sy=(h+expand*2)/h,sz=(d+expand*2)/d;
    return [
      {name:'front',uv:[u+d,v+d],w,h,n:[0,0,1],point:(a,b)=>[x+a*sx,y+(h-b)*sy,z+d*sz]},
      {name:'back',uv:[u+2*d+w,v+d],w,h,n:[0,0,-1],point:(a,b)=>[x+(w-a)*sx,y+(h-b)*sy,z]},
      {name:'right',uv:[u,v+d],w:d,h,n:[-1,0,0],point:(a,b)=>[x,y+(h-b)*sy,z+a*sz]},
      {name:'left',uv:[u+d+w,v+d],w:d,h,n:[1,0,0],point:(a,b)=>[x+w*sx,y+(h-b)*sy,z+(d-a)*sz]},
      {name:'top',uv:[u+d,v],w,h:d,n:[0,1,0],point:(a,b)=>[x+a*sx,y+h*sy,z+b*sz]},
      {name:'bottom',uv:[u+d+w,v],w,h:d,n:[0,-1,0],point:(a,b)=>[x+a*sx,y,z+(d-b)*sz]}
    ];
  }
  function buildMesh(texture, model = 'default') {
    if (texture.width !== 64 || ![32,64].includes(texture.height) || !['default','slim'].includes(model) || (texture.height === 32 && model === 'slim')) throw new Error('皮肤尺寸或手臂模型不匹配。');
    const legacy=texture.height===32, mesh=[];
    let opaque=true;
    for(let i=3;i<texture.data.length;i+=4) if(texture.data[i]!==255){opaque=false;break;}
    for(const p of parts(model,legacy)) {
      for(const outer of [false,true]) {
        if(outer && (opaque || (legacy && p.name!=='head')))continue;
        const fs=faces(p,outer?p.overlay:p.uv,outer?(p.expand||.25):0);
        for(const f of fs) {
          const source=p.mirror && !outer ? fs.find(g=>g.name===({left:'right',right:'left'}[f.name]||f.name)) : f;
          for(let b=0;b<f.h;b++)for(let a=0;a<f.w;a++){
            const px=source.uv[0]+(p.mirror&&!outer?f.w-1-a:a),py=source.uv[1]+b;
            const i=(py*64+px)*4,alpha=outer?texture.data[i+3]:255;
            if(!alpha)continue;
            mesh.push({part:p.name,face:f.name,outer,uv:[px,py],n:f.n,
              points:[f.point(a,b),f.point(a+1,b),f.point(a+1,b+1),f.point(a,b+1)],
              rgba:[texture.data[i],texture.data[i+1],texture.data[i+2],alpha]});
          }
        }
      }
    }
    return mesh;
  }
  function draw(canvas, mesh, yaw = -.46) {
    const ctx=canvas.getContext('2d');
    if(!ctx)throw new Error('浏览器无法绘制模型。');
    ctx.clearRect(0,0,canvas.width,canvas.height);
    const scale=canvas.height/40,cx=canvas.width/2,base=canvas.height*.88;
    const cs=Math.cos(yaw),sn=Math.sin(yaw),cp=Math.cos(.20),sp=Math.sin(.20);
    const transform=([x,y,z])=>[x*cs+z*sn,y*cp-(z*cs-x*sn)*sp,y*sp+(z*cs-x*sn)*cp];
    const polygons=[];
    for(const tile of mesh){
      const normal=transform(tile.n);
      if(!tile.outer && normal[2]<=0)continue;
      const vertices=tile.points.map(transform);
      const light=Math.max(.55, Math.min(1.08,.78+normal[1]*.22+normal[2]*.2));
      const color=tile.rgba.slice(0,3).map(c=>Math.min(255,Math.round(c*light)));
      polygons.push({vertices,depth:vertices.reduce((sum,p)=>sum+p[2],0)/4,color:`rgba(${color.join(',')},${tile.rgba[3]/255})`,alpha:tile.rgba[3]});
    }
    polygons.sort((a,b)=>a.depth-b.depth);
    for(const p of polygons){
      ctx.fillStyle=p.color;ctx.beginPath();
      p.vertices.forEach(([x,y],i)=>i?ctx.lineTo(cx+x*scale,base-y*scale):ctx.moveTo(cx+x*scale,base-y*scale));ctx.closePath();ctx.fill();
      // Cover subpixel seams only on opaque pixels; alpha edges must not be painted twice.
      if(p.alpha===255){ctx.strokeStyle=p.color;ctx.lineWidth=.65;ctx.stroke();}
    }
  }
  function mannequin() {
    const data=new Uint8ClampedArray(64*64*4);
    for(let y=0;y<64;y++)for(let x=0;x<64;x++){
      const i=(y*64+x)*4,c=((x*17+y*11)%19===0?178:190);
      data.set([c,c+2,c+3,255],i);
    }
    // A neutral display model, not a claim about the player's Minecraft default skin.
    return {width:64,height:64,data};
  }
  async function load(url) {
    const image=new Image();image.src=url;
    await image.decode();
    if(image.width!==64 || ![32,64].includes(image.height))throw new Error('请使用 64×64 或 64×32 PNG 皮肤。');
    const surface=document.createElement('canvas');surface.width=64;surface.height=image.height;
    const ctx=surface.getContext('2d',{willReadFrequently:true});
    if(!ctx)throw new Error('浏览器无法读取皮肤。');
    ctx.drawImage(image,0,0);
    return ctx.getImageData(0,0,64,image.height);
  }
  function avatar(canvas, texture) {
    const ctx=canvas.getContext('2d');if(!ctx)return;
    const scale=canvas.width/8;ctx.clearRect(0,0,canvas.width,canvas.height);
    let opaque=true;for(let i=3;i<texture.data.length;i+=4)if(texture.data[i]!==255){opaque=false;break;}
    for(const offset of (opaque?[8]:[8,40]))for(let y=0;y<8;y++)for(let x=0;x<8;x++){
      const i=((y+8)*64+x+offset)*4;
      ctx.fillStyle=`rgba(${texture.data[i]},${texture.data[i+1]},${texture.data[i+2]},${offset===8?1:texture.data[i+3]/255})`;
      ctx.fillRect(x*scale,y*scale,scale,scale);
    }
  }
  const api={parts,faces,buildMesh,draw,load,mannequin,avatar};
  if(typeof module!=='undefined' && module.exports)module.exports=api;
  else window.AttoSkin=api;
})();
