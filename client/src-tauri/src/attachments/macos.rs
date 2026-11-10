use super::*;
use objc2::AnyThread;
use objc2_app_kit::{
    NSBitmapImageFileType, NSBitmapImageRep, NSPasteboard, NSPasteboardTypeFileURL,
    NSPasteboardTypePNG, NSPasteboardTypeTIFF,
};
use objc2_foundation::NSDictionary;

pub(crate) fn read_clipboard() -> Result<ClipboardContent> {
    let board = NSPasteboard::generalPasteboard();
    let mut paths = Vec::new();
    if let Some(items) = board.pasteboardItems() {
        for item in items.iter() {
            if let Some(text) = item.stringForType(unsafe { NSPasteboardTypeFileURL }) {
                let url = reqwest::Url::parse(&text.to_string()).map_err(|_| image_error())?;
                if url.scheme() != "file"
                    || url
                        .host_str()
                        .is_some_and(|h| !h.is_empty() && h != "localhost")
                {
                    return Err(TransferError::new("只支持本机复制的文件"));
                }
                paths.push(
                    url.to_file_path()
                        .map_err(|_| TransferError::new("剪贴板文件路径无效"))?,
                );
                if paths.len() > 20 {
                    return Err(TransferError::new("一次最多粘贴20个文件"));
                }
            }
        }
    }
    if !paths.is_empty() {
        return Ok(ClipboardContent::Files(paths));
    }
    if let Some(data) = board.dataForType(unsafe { NSPasteboardTypePNG }) {
        if data.length() > MAX_IMAGE_BYTES {
            return Err(image_error());
        }
        return Ok(ClipboardContent::Image(data.to_vec()));
    }
    if let Some(data) = board.dataForType(unsafe { NSPasteboardTypeTIFF }) {
        if data.length() > MAX_IMAGE_BYTES {
            return Err(image_error());
        }
        bounded_tiff(&data.to_vec())?;
        let image = NSBitmapImageRep::initWithData(NSBitmapImageRep::alloc(), &data)
            .ok_or_else(image_error)?;
        if image.pixelsWide() <= 0 || image.pixelsHigh() <= 0 {
            return Err(image_error());
        }
        dimensions(image.pixelsWide() as u32, image.pixelsHigh() as u32)?;
        let png = unsafe {
            image.representationUsingType_properties(
                NSBitmapImageFileType::PNG,
                &NSDictionary::new(),
            )
        }
        .ok_or_else(image_error)?;
        if png.length() > MAX_IMAGE_BYTES {
            return Err(image_error());
        }
        return Ok(ClipboardContent::Image(png.to_vec()));
    }
    Ok(ClipboardContent::Empty)
}
// Validate the classic TIFF dimensions before asking AppKit to decode. BigTIFF and
// multipart image documents are deliberately not accepted as clipboard screenshots.
fn bounded_tiff(data: &[u8]) -> Result<()> {
    if data.len() < 8 {
        return Err(image_error());
    }
    let little = match &data[..2] {
        b"II" => true,
        b"MM" => false,
        _ => return Err(image_error()),
    };
    let u16at = |n: usize| -> Result<u16> {
        let v: [u8; 2] = data
            .get(n..n + 2)
            .ok_or_else(image_error)?
            .try_into()
            .unwrap();
        Ok(if little {
            u16::from_le_bytes(v)
        } else {
            u16::from_be_bytes(v)
        })
    };
    let u32at = |n: usize| -> Result<u32> {
        let v: [u8; 4] = data
            .get(n..n + 4)
            .ok_or_else(image_error)?
            .try_into()
            .unwrap();
        Ok(if little {
            u32::from_le_bytes(v)
        } else {
            u32::from_be_bytes(v)
        })
    };
    if u16at(2)? != 42 {
        return Err(image_error());
    }
    let offset = u32at(4)? as usize;
    let count = u16at(offset)? as usize;
    if count > 256
        || offset
            .checked_add(2 + count * 12 + 4)
            .is_none_or(|n| n > data.len())
    {
        return Err(image_error());
    }
    let (mut width, mut height) = (None, None);
    for i in 0..count {
        let p = offset + 2 + i * 12;
        let tag = u16at(p)?;
        if matches!(tag, 256 | 257) {
            if u32at(p + 4)? != 1 {
                return Err(image_error());
            }
            let value = match u16at(p + 2)? {
                3 => u16at(p + 8)? as u32,
                4 => u32at(p + 8)?,
                _ => return Err(image_error()),
            };
            if tag == 256 {
                width = Some(value);
            } else {
                height = Some(value);
            }
        }
    }
    dimensions(
        width.ok_or_else(image_error)?,
        height.ok_or_else(image_error)?,
    )?;
    if u32at(offset + 2 + count * 12)? != 0 {
        return Err(image_error());
    }
    Ok(())
}
